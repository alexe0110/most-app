package docsbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// pageSize is how many documents one catalog page shows (#13).
	pageSize = 10
	// searchLimit caps search results (#14).
	searchLimit = 10
	// exportLimit caps how many documents one "send all" request resends (#15).
	exportLimit = 50
	// statusWindow is how long an open status message keeps absorbing new documents.
	statusWindow = 30 * time.Second
	// sweepBatch caps how many stuck uploads one sweeper run handles.
	sweepBatch = 100
	// sweepStaleAfter is how long a document may sit in pending/uploading before
	// the sweeper requeues its upload.
	sweepStaleAfter = 30 * time.Minute
	// sweepFailAfter is how long a document may stay stuck before it is failed.
	sweepFailAfter = 24 * time.Hour
	// maxQueryRunes caps a search query's length.
	maxQueryRunes = 100
	// MaxDriveBytes is the largest file the bot uploads to Drive; a bigger one
	// stays in Telegram (backup_status too_large).
	MaxDriveBytes = 20 << 20
	// maxPendingPerUser caps files awaiting a category per user.
	maxPendingPerUser = 20
	// maxFilesPerMinute caps how many files one user may send per minute.
	maxFilesPerMinute = 30
	// MaxDocumentsPerUser caps one user's total documents.
	MaxDocumentsPerUser = 5000
	// intakeWindow is the flood-check window.
	intakeWindow = time.Minute
	// pendingTTL is how long a document may wait for a category before it is purged.
	pendingTTL = 24 * time.Hour
	// remoteCheckTimeout bounds the Drive existence check made when a card opens.
	remoteCheckTimeout = 3 * time.Second
	// remoteCheckEvery is the minimum gap between existence checks of one document.
	remoteCheckEvery = time.Minute
)

// Service holds the Most Docs use cases. It depends only on ports, never on
// Telegram, Postgres or Drive SDKs.
type Service struct {
	repo    Repo
	drive   Drive
	files   FileSource
	uploads Uploads
	folders Folders
	status  StatusMessages
	syncs   Syncs
	now     func() time.Time
	loc     *time.Location

	// checks remembers when each document's Drive file was last looked up, to
	// throttle OpenCard; guarded by checksMu.
	checksMu sync.Mutex
	checks   map[DocumentID]time.Time

	pages PageCounter // nil: pages are not counted
}

// Option tunes a Service at construction.
type Option func(*Service)

// WithClock replaces the time source; used to pin the status-message window in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithPageCounter sets how the pages of a PDF are counted when it is backed up.
// Without it the page count of such a document stays unknown.
func WithPageCounter(pc PageCounter) Option {
	return func(s *Service) { s.pages = pc }
}

// WithLocation sets the time zone used for the date in default titles.
func WithLocation(loc *time.Location) Option {
	return func(s *Service) { s.loc = loc }
}

// NewService wires the service around its ports.
func NewService(repo Repo, drive Drive, files FileSource, uploads Uploads, folders Folders, status StatusMessages, syncs Syncs, opts ...Option) *Service {
	s := &Service{
		repo: repo, drive: drive, files: files, uploads: uploads, folders: folders, status: status, syncs: syncs,
		now: time.Now,
		loc: defaultLocation(),

		checks: make(map[DocumentID]time.Time),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Receive stores a freshly sent file as pending and asks for a category. A file
// already saved returns AlreadySaved; a still-pending one re-asks the category.
func (s *Service) Receive(ctx context.Context, in IncomingFile) (Reply, error) {
	in.Title = NormalizeTitle(in.Title)
	existing, err := s.repo.DocumentByFileUniqueID(ctx, in.UserID, in.FileUniqueID)
	switch {
	case err == nil && existing.Status == StatusSaved:
		return AlreadySaved{Doc: existing}, nil
	case err == nil:
		return NeedCategory{Doc: existing}, nil
	case !errors.Is(err, ErrNotFound):
		return nil, fmt.Errorf("lookup document: %w", err)
	}

	stats, err := s.repo.IntakeStats(ctx, in.UserID, s.now().Add(-intakeWindow))
	if err != nil {
		return nil, fmt.Errorf("intake stats: %w", err)
	}
	switch {
	case stats.Total >= MaxDocumentsPerUser:
		return QuotaExceeded{}, nil
	case stats.Recent >= maxFilesPerMinute:
		return RateLimited{}, nil
	case stats.Pending >= maxPendingPerUser:
		return TooManyPending{}, nil
	}

	doc, err := s.repo.CreateDocument(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return NeedCategory{Doc: doc}, nil
}

// SaveDerived stores a file made from another document (a compressed copy, a
// PDF) as a new document. When the source is saved, the copy lands in the
// source's category under DerivedTitle(source title, suffix) and the result is
// Saved. A pending or absent source (empty sourceID) has no category to inherit:
// the copy is saved in CatOther under in.Title, and the user can change the
// category from the card. Limit replies and duplicates pass through unchanged;
// an unknown source is ErrNotFound.
func (s *Service) SaveDerived(ctx context.Context, user UserID, sourceID DocumentID, in IncomingFile, suffix string) (Reply, error) {
	in.UserID = user
	var src Document
	fromSaved := false
	if sourceID != "" {
		var err error
		src, err = s.repo.DocumentByID(ctx, user, sourceID)
		if err != nil {
			return nil, fmt.Errorf("load source document: %w", err)
		}
		fromSaved = src.Status == StatusSaved
	}
	if fromSaved {
		in.Title = DerivedTitle(src.Title, suffix)
	}

	rep, err := s.Receive(ctx, in)
	if err != nil {
		return nil, err
	}
	need, ok := rep.(NeedCategory)
	if !ok {
		return rep, nil
	}
	cat := CatOther
	if fromSaved {
		cat = src.Category
	}
	saved, err := s.ChooseCategory(ctx, user, need.Doc.ID, cat)
	if err != nil {
		// Don't leave the copy pending: nobody will categorize it.
		if delErr := s.repo.DeleteDocument(ctx, user, need.Doc.ID); delErr != nil && !errors.Is(delErr, ErrNotFound) {
			return nil, errors.Join(err, fmt.Errorf("discard derived document: %w", delErr))
		}
		return nil, err
	}
	return saved, nil
}

// DiscardAlbum deletes the still-pending documents of an album, e.g. after its
// photos were merged into one PDF. Saved documents are never touched. It returns
// how many were removed; a document purged in parallel is skipped.
func (s *Service) DiscardAlbum(ctx context.Context, user UserID, group string) (int, error) {
	if group == "" {
		return 0, nil
	}
	pending, err := s.repo.PendingByMediaGroup(ctx, user, group)
	if err != nil {
		return 0, fmt.Errorf("list album: %w", err)
	}
	n := 0
	for _, d := range pending {
		err := s.repo.DeleteDocument(ctx, user, d.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("delete album document: %w", err)
		}
		n++
	}
	return n, nil
}

// PurgeStalePending deletes pending documents nobody categorized within the TTL
// and returns how many were removed. It is the purge_pending job's use case.
func (s *Service) PurgeStalePending(ctx context.Context) (int, error) {
	n, err := s.repo.PurgeStalePending(ctx, s.now().Add(-pendingTTL))
	if err != nil {
		return 0, fmt.Errorf("purge stale pending: %w", err)
	}
	return n, nil
}

// ChooseCategoryGroup saves every pending document of the album that anyDocID
// belongs to under cat. The album lives in the database, so it survives a
// restart. A document without an album is saved alone. It is idempotent like
// ChooseCategory: once nothing is pending the result is flagged Already.
func (s *Service) ChooseCategoryGroup(ctx context.Context, user UserID, anyDocID DocumentID, cat Category) (SavedGroup, error) {
	if !cat.Valid() {
		return SavedGroup{}, ErrUnknownCategory
	}
	anchor, err := s.repo.DocumentByID(ctx, user, anyDocID)
	if err != nil {
		return SavedGroup{}, fmt.Errorf("load document: %w", err)
	}
	ids := []DocumentID{anchor.ID}
	if anchor.MediaGroupID != "" {
		pending, err := s.repo.PendingByMediaGroup(ctx, user, anchor.MediaGroupID)
		if err != nil {
			return SavedGroup{}, fmt.Errorf("list album: %w", err)
		}
		if len(pending) > 0 {
			ids = ids[:0]
			for _, d := range pending {
				ids = append(ids, d.ID)
			}
		}
	}
	var out SavedGroup
	for _, id := range ids {
		saved, err := s.ChooseCategory(ctx, user, id, cat)
		if err != nil {
			return out, err
		}
		if saved.Already {
			continue
		}
		out.Docs = append(out.Docs, saved.Doc)
		out.OfferDrive = out.OfferDrive || saved.OfferDrive
	}
	out.Already = len(out.Docs) == 0
	return out, nil
}

// ChooseCategory saves a pending document under cat. It is idempotent: a second
// tap on an already-saved document returns it flagged Already. When Drive is connected the
// document is queued for upload (the upload_document worker performs it); otherwise it stays in
// Telegram and we may offer to connect Drive.
func (s *Service) ChooseCategory(ctx context.Context, user UserID, id DocumentID, cat Category) (Saved, error) {
	if !cat.Valid() {
		return Saved{}, ErrUnknownCategory
	}
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Saved{}, fmt.Errorf("load document: %w", err)
	}
	if doc.Status == StatusSaved {
		return Saved{Doc: doc, Already: true}, nil
	}

	won, err := s.repo.SaveDocument(ctx, user, id, cat)
	if err != nil {
		return Saved{}, fmt.Errorf("save document: %w", err)
	}
	if !won {
		// Lost a race: a parallel tap saved it, or the purge job deleted it.
		cur, err := s.repo.DocumentByID(ctx, user, id)
		if err != nil {
			return Saved{}, fmt.Errorf("reload document: %w", err)
		}
		return Saved{Doc: cur, Already: true}, nil
	}
	doc.Category = cat
	doc.Status = StatusSaved

	if strings.TrimSpace(doc.Title) == "" {
		title, err := s.defaultTitle(ctx, user, cat)
		if err != nil {
			return Saved{}, err
		}
		if err := s.repo.SetTitle(ctx, user, id, title); err != nil {
			return Saved{}, fmt.Errorf("set default title: %w", err)
		}
		doc.Title = title
	}

	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		if doc.FileSize > MaxDriveBytes {
			// Known to be above the limit: skip the doomed download, keep it in Telegram.
			if err := s.repo.SetBackupStatus(ctx, user, id, BackupTooLarge); err != nil {
				return Saved{}, fmt.Errorf("mark too large: %w", err)
			}
			doc.BackupStatus = BackupTooLarge
			return Saved{Doc: doc}, nil
		}
		if err := s.uploads.EnqueueUpload(ctx, user, id); err != nil {
			return Saved{}, fmt.Errorf("enqueue upload: %w", err)
		}
		doc.BackupStatus = BackupPending
		return Saved{Doc: doc}, nil
	}

	count, err := s.repo.CountSaved(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("count saved: %w", err)
	}
	return Saved{Doc: doc, OfferDrive: count == 1 || count == 5}, nil
}

// defaultTitle builds "<category> — dd.mm.yyyy", adding " (n)" until it is unique
// within the user's category.
func (s *Service) defaultTitle(ctx context.Context, user UserID, cat Category) (string, error) {
	base := cat.Label() + " — " + s.now().In(s.loc).Format("02.01.2006")
	title := base
	for i := 2; i <= maxTitleSuffix; i++ {
		exists, err := s.repo.TitleExistsInCategory(ctx, user, cat, title)
		if err != nil {
			return "", fmt.Errorf("check title collision: %w", err)
		}
		if !exists {
			return title, nil
		}
		title = fmt.Sprintf("%s (%d)", base, i)
	}
	return title, nil // titles are not unique-constrained: accept the last candidate
}

// maxTitleSuffix bounds the default-title collision search.
const maxTitleSuffix = 100

// BackupResult is the outcome of a Drive upload attempt.
type BackupResult struct {
	Doc      Document
	TooLarge bool
	// Noop true means the worker should finish silently: there is nothing to do
	// (document gone or already backed up) and the user is not notified.
	Noop bool
	// OrphanTrashErr is set when the document was deleted mid-upload and trashing
	// the uploaded file failed: the file stays in Drive as an orphan.
	OrphanTrashErr error
}

// Backup uploads a saved document to the user's Drive. It is the upload_document
// worker's use case and is idempotent: an already backed-up document is a no-op.
// A file above the download limit is marked too_large and left in Telegram; a
// revoked grant returns ErrDriveRevoked and a full Drive ErrDriveFull; transient errors are returned for retry.
func (s *Service) Backup(ctx context.Context, user UserID, id DocumentID) (BackupResult, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		// Deleted while the job was queued: finish without retries.
		return BackupResult{Noop: true}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("load document: %w", err)
	}
	if doc.BackupStatus == BackupBackedUp {
		return BackupResult{Doc: doc, Noop: true}, nil
	}
	startVersion := doc.MetaVersion
	// Do not bump updated_at here: a poisoned document that crashes every
	// attempt must still age out to failed via the sweeper's 24h backstop.
	if err := s.repo.MarkUploading(ctx, user, id); err != nil {
		return BackupResult{}, fmt.Errorf("mark uploading: %w", err)
	}

	data, mime, err := s.files.Download(ctx, doc.TgFileID)
	if errors.Is(err, ErrFileTooLarge) {
		if err := s.repo.SetBackupStatus(ctx, user, id, BackupTooLarge); err != nil {
			return BackupResult{}, fmt.Errorf("mark too large: %w", err)
		}
		doc.BackupStatus = BackupTooLarge
		return BackupResult{Doc: doc, TooLarge: true}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("download file: %w", err)
	}

	pages := s.countPages(ctx, doc, mime, data)
	name := RemoteName(doc.Title, doc.MIME, doc.Kind)
	file, err := s.drive.Upload(ctx, user, DriveUpload{Category: doc.Category, Name: name, MIME: mime, Data: data, DocID: string(id)})
	if errors.Is(err, ErrDriveDisconnected) {
		// User disconnected Drive mid-upload: drop back to Telegram-only, finish silently.
		if serr := s.repo.SetBackupStatus(ctx, user, id, BackupNone); serr != nil {
			return BackupResult{}, fmt.Errorf("reset backup status: %w", serr)
		}
		return BackupResult{Noop: true}, nil
	}
	if errors.Is(err, ErrDriveRevoked) {
		return BackupResult{}, ErrDriveRevoked
	}
	if errors.Is(err, ErrDriveFull) {
		return BackupResult{}, ErrDriveFull
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("upload to drive: %w", err)
	}

	queued, err := s.syncs.MarkBackedUp(ctx, user, id, file.ID, startVersion, pages)
	if errors.Is(err, ErrNotFound) {
		// Deleted while the file was uploading: nobody references the file, so
		// trash it (best effort; a failure leaves an orphan the user can delete
		// and is reported to the worker to log).
		return BackupResult{Noop: true, OrphanTrashErr: s.drive.Trash(ctx, user, file.ID)}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("mark backed up: %w", err)
	}
	doc.BackupStatus = BackupBackedUp
	doc.DriveFileID = file.ID
	if pages > 0 {
		doc.PageCount = pages
	}
	if queued {
		doc.SyncStatus = SyncPending
	}
	return BackupResult{Doc: doc}, nil
}

// countPages counts a PDF's pages for the card; 0 means unknown (not a PDF, no
// counter configured, or the file could not be read). A failure is not an error:
// the page count is decoration and must not hold up the upload.
func (s *Service) countPages(ctx context.Context, doc Document, mime string, data []byte) int {
	if s.pages == nil || (doc.MIME != "application/pdf" && mime != "application/pdf") {
		return 0
	}
	n, err := s.pages.PageCount(ctx, data)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ReplaceContent swaps the file of a saved document for in (a converted copy of
// it), keeping id, title and category. The user's other document with the same
// file is AlreadySaved and nothing changes; the same file as the current one is
// Replaced unchanged. A backed-up document is queued for a cloud sync; one that
// is not on Drive changes in the database only, and its upload takes the new file.
// A document that is uploading or syncing is ReplaceBusy, one whose Drive file is
// gone ReplaceMissing, an unknown one ErrNotFound.
func (s *Service) ReplaceContent(ctx context.Context, user UserID, id DocumentID, in IncomingFile) (Reply, error) {
	in.UserID = user
	cur, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("load document: %w", err)
	}
	if cur.TgFileUniqueID == in.FileUniqueID {
		return Replaced{Doc: cur}, nil
	}
	if other, err := s.repo.DocumentByFileUniqueID(ctx, user, in.FileUniqueID); err == nil {
		if other.ID == id {
			return Replaced{Doc: other}, nil // a parallel tap already swapped it
		}
		return AlreadySaved{Doc: other}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("lookup document: %w", err)
	}

	// Without Drive only the row changes, like a rename: the Drive file stays old.
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("drive status: %w", err)
	}
	replaced, err := s.syncs.ReplaceContent(ctx, user, id, in, connected)
	if errors.Is(err, ErrDuplicateFile) {
		// Lost a race with a save of the same file.
		other, lerr := s.repo.DocumentByFileUniqueID(ctx, user, in.FileUniqueID)
		if lerr != nil {
			return nil, fmt.Errorf("lookup duplicate: %w", lerr)
		}
		return AlreadySaved{Doc: other}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("replace content: %w", err)
	}
	if !replaced {
		return s.replaceRefused(ctx, user, id)
	}
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}
	return Replaced{Doc: doc}, nil
}

// replaceRefused explains why the guarded update touched no row.
func (s *Service) replaceRefused(ctx context.Context, user UserID, id DocumentID) (Reply, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}
	if doc.SyncStatus == SyncMissing {
		return ReplaceMissing{}, nil
	}
	// Uploading, or a sync in flight; if the state changed back between the two
	// statements, a retry by the user will succeed.
	return ReplaceBusy{}, nil
}

// MarkBackupFailed records a terminal upload failure with its reason
// (BackupReason*; "" is stored as unknown and shown as unavailable).
func (s *Service) MarkBackupFailed(ctx context.Context, user UserID, id DocumentID, reason BackupReason) error {
	if err := s.repo.FailBackup(ctx, user, id, reason); err != nil {
		return fmt.Errorf("mark backup failed: %w", err)
	}
	return nil
}

// SyncResult is the outcome of a cloud sync attempt.
type SyncResult struct {
	Doc Document
	// Noop true means there was nothing to do (document gone, not backed up, or
	// already in sync); the worker finishes silently.
	Noop bool
	// Missing true means the Drive file is gone; the document is now sync_status
	// missing and the user can re-upload it.
	Missing bool
}

// SyncRemote makes the cloud file match the document row: content (when dirty),
// category folder and name. It is the storage_sync worker's use case, idempotent
// and safe to retry. A revoked grant returns ErrDriveRevoked; other errors are
// returned for retry.
func (s *Service) SyncRemote(ctx context.Context, user UserID, id DocumentID) (SyncResult, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		return SyncResult{Noop: true}, nil
	}
	if err != nil {
		return SyncResult{}, fmt.Errorf("load document: %w", err)
	}
	if doc.BackupStatus != BackupBackedUp || doc.DriveFileID == "" || doc.SyncStatus == SyncMissing ||
		(doc.SyncStatus == SyncOK && !doc.ContentDirty) {
		return SyncResult{Doc: doc, Noop: true}, nil
	}

	err = s.pushToRemote(ctx, user, doc)
	switch {
	case errors.Is(err, ErrRemoteMissing):
		if serr := s.repo.SetSyncStatus(ctx, user, id, SyncMissing, ""); serr != nil {
			return SyncResult{}, fmt.Errorf("mark sync missing: %w", serr)
		}
		doc.SyncStatus = SyncMissing
		return SyncResult{Doc: doc, Missing: true}, nil
	case errors.Is(err, ErrDriveRevoked):
		return SyncResult{}, ErrDriveRevoked
	case errors.Is(err, ErrDriveDisconnected):
		if serr := s.repo.SetSyncStatus(ctx, user, id, SyncFailed, ""); serr != nil {
			return SyncResult{}, fmt.Errorf("mark sync failed: %w", serr)
		}
		doc.SyncStatus = SyncFailed
		return SyncResult{Doc: doc, Noop: true}, nil
	case err != nil:
		return SyncResult{}, err
	}

	won, err := s.repo.MarkSynced(ctx, user, id, doc.MetaVersion)
	if err != nil {
		return SyncResult{}, fmt.Errorf("mark synced: %w", err)
	}
	if won {
		doc.SyncStatus, doc.ContentDirty = SyncOK, false
		return SyncResult{Doc: doc}, nil
	}
	// The row changed while we worked: run again for the newer state, unless the
	// document is gone or no longer backed up.
	cur, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		return SyncResult{Doc: doc, Noop: true}, nil
	}
	if err != nil {
		return SyncResult{}, fmt.Errorf("reload document: %w", err)
	}
	if cur.BackupStatus == BackupBackedUp {
		if err := s.syncs.EnqueueSync(ctx, user, id); err != nil {
			return SyncResult{}, fmt.Errorf("enqueue sync: %w", err)
		}
	}
	return SyncResult{Doc: cur}, nil
}

// pushToRemote applies the document's content, folder and name to the remote file.
func (s *Service) pushToRemote(ctx context.Context, user UserID, doc Document) error {
	if doc.ContentDirty {
		data, mime, err := s.files.Download(ctx, doc.TgFileID)
		if err != nil {
			return fmt.Errorf("download file: %w", err)
		}
		if err := s.drive.Replace(ctx, user, doc.DriveFileID, mime, data); err != nil {
			return fmt.Errorf("replace remote content: %w", err)
		}
	}
	if err := s.drive.Move(ctx, user, doc.DriveFileID, doc.Category); err != nil {
		return fmt.Errorf("move remote file: %w", err)
	}
	name := RemoteName(doc.Title, doc.MIME, doc.Kind)
	if err := s.drive.Rename(ctx, user, doc.DriveFileID, name); err != nil {
		return fmt.Errorf("rename remote file: %w", err)
	}
	return nil
}

// MarkSyncFailed records a terminal sync failure; reason is "" or SyncReasonRevoked.
func (s *Service) MarkSyncFailed(ctx context.Context, user UserID, id DocumentID, reason string) error {
	if err := s.repo.SetSyncStatus(ctx, user, id, SyncFailed, reason); err != nil {
		return fmt.Errorf("mark sync failed: %w", err)
	}
	return nil
}

// RetrySync re-queues a failed sync. Any other state or a foreign document
// returns ErrNotRetryable.
func (s *Service) RetrySync(ctx context.Context, user UserID, id DocumentID) error {
	queued, err := s.syncs.RetrySync(ctx, user, id)
	if err != nil {
		return fmt.Errorf("retry sync: %w", err)
	}
	if !queued {
		return ErrNotRetryable
	}
	return nil
}

// TrashRemote moves a deleted document's cloud file to the trash. It is the
// storage_trash worker's use case; a file that is already gone counts as done.
func (s *Service) TrashRemote(ctx context.Context, user UserID, remoteID string) error {
	err := s.drive.Trash(ctx, user, remoteID)
	if err != nil && !errors.Is(err, ErrRemoteMissing) {
		return err
	}
	return nil
}

// SweepResult summarizes one sweeper run.
type SweepResult struct {
	Found    int
	Requeued int
	Failed   []StuckUpload
	// SyncsRequeued and SyncsFailed count documents whose cloud sync was stuck.
	SyncsRequeued int
	SyncsFailed   int
}

// SweepStuckUploads requeues uploads stuck mid-flight and fails the ones stuck
// for too long. Requeueing does not touch backup_status/updated_at, so the
// stuck-since clock keeps running; a live job dedups to a no-op.
func (s *Service) SweepStuckUploads(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	now := s.now()
	rows, err := s.repo.StuckUploads(ctx, now.Add(-sweepStaleAfter), sweepBatch)
	if err != nil {
		return res, fmt.Errorf("list stuck uploads: %w", err)
	}
	res.Found = len(rows)
	for _, r := range rows {
		if now.Sub(r.UpdatedAt) >= sweepFailAfter {
			if err := s.repo.FailBackup(ctx, r.User, r.ID, BackupReasonUnavailable); err != nil {
				return res, fmt.Errorf("fail stuck upload: %w", err)
			}
			res.Failed = append(res.Failed, r)
			continue
		}
		if err := s.uploads.RequeueUpload(ctx, r.User, r.ID); err != nil {
			return res, fmt.Errorf("requeue stuck upload: %w", err)
		}
		res.Requeued++
	}
	return s.sweepStuckSyncs(ctx, now, res)
}

// sweepStuckSyncs recovers syncs left pending (a worker died, or failing the
// sync failed on the last attempt): requeue them, and fail the ones stuck for too
// long. Requeueing leaves updated_at alone, so the clock keeps running.
func (s *Service) sweepStuckSyncs(ctx context.Context, now time.Time, res SweepResult) (SweepResult, error) {
	rows, err := s.repo.StuckSyncs(ctx, now.Add(-sweepStaleAfter), sweepBatch)
	if err != nil {
		return res, fmt.Errorf("list stuck syncs: %w", err)
	}
	for _, r := range rows {
		if now.Sub(r.UpdatedAt) >= sweepFailAfter {
			if err := s.repo.SetSyncStatus(ctx, r.User, r.ID, SyncFailed, ""); err != nil {
				return res, fmt.Errorf("fail stuck sync: %w", err)
			}
			res.SyncsFailed++
			continue
		}
		if err := s.syncs.RequeueSync(ctx, r.User, r.ID); err != nil {
			return res, fmt.Errorf("requeue stuck sync: %w", err)
		}
		res.SyncsRequeued++
	}
	return res, nil
}

// TransferSaved moves every saved document not yet backed up to Drive, tracked by
// one transfer status message that takes over the given Telegram message. ok is
// false when there is nothing to transfer.
func (s *Service) TransferSaved(ctx context.Context, user UserID, chatID int64, messageID int) (StatusView, bool, error) {
	ids, err := s.repo.ClaimTransferable(ctx, user)
	if err != nil {
		return StatusView{}, false, fmt.Errorf("claim not backed up: %w", err)
	}
	return s.startGroupedUpload(ctx, user, chatID, messageID, ids)
}

// NotBackedUpCount reports how many saved documents are not yet on Drive.
func (s *Service) NotBackedUpCount(ctx context.Context, user UserID) (int, error) {
	n, err := s.repo.CountNotBackedUp(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("count not backed up: %w", err)
	}
	return n, nil
}

// ResetDriveFolders clears the user's cached Drive folder ids, so folders are
// found or created afresh (the Drive may now belong to another Google account).
func (s *Service) ResetDriveFolders(ctx context.Context, user UserID) error {
	if err := s.repo.DeleteAllFolders(ctx, user); err != nil {
		return fmt.Errorf("delete all folders: %w", err)
	}
	return nil
}

// DriveConnected reports whether the user's Drive is connected.
func (s *Service) DriveConnected(ctx context.Context, user UserID) (bool, error) {
	ok, err := s.drive.Connected(ctx, user)
	if err != nil {
		return false, fmt.Errorf("drive status: %w", err)
	}
	return ok, nil
}

// RetryBackup re-enqueues a failed document's Drive upload. Any other state
// (done, in flight, already retried) or a foreign document returns ErrNotRetryable.
func (s *Service) RetryBackup(ctx context.Context, user UserID, id DocumentID) error {
	queued, err := s.claimAndRequeue(ctx, user, id)
	if err != nil {
		return err
	}
	if !queued {
		return ErrNotRetryable
	}
	return nil
}

// claimAndRequeue atomically moves a failed document to pending and queues its
// upload. queued is false when the document was not failed (nothing is done).
func (s *Service) claimAndRequeue(ctx context.Context, user UserID, id DocumentID) (queued bool, err error) {
	claimed, err := s.repo.ClaimFailed(ctx, user, id)
	if err != nil {
		return false, fmt.Errorf("claim failed upload: %w", err)
	}
	if !claimed {
		return false, nil
	}
	if err := s.uploads.RequeueUpload(ctx, user, id); err != nil {
		return false, fmt.Errorf("requeue upload: %w", err)
	}
	return true, nil
}

// Settings reports the Drive connection and the user's document counts for /settings.
func (s *Service) Settings(ctx context.Context, user UserID) (SettingsView, error) {
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("drive status: %w", err)
	}
	counts, err := s.repo.BackupStatusCounts(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("backup status counts: %w", err)
	}
	inline, err := s.repo.InlineEnabled(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("inline enabled: %w", err)
	}
	view := SettingsView{DriveConnected: connected, InlineEnabled: inline}
	for _, c := range counts {
		view.Total += c.Count
		switch c.Status {
		case BackupBackedUp:
			view.InDrive += c.Count
		case BackupFailed:
			view.Failed += c.Count
		}
	}
	view.OnlyTelegram = view.Total - view.InDrive - view.Failed
	return view, nil
}

// Disconnect turns Drive off at the user's request, cancels the user's pending
// uploads (sync and trash jobs stay: their workers see the disconnect and settle
// the document) and resets stuck documents back to Telegram-only. Google failures are
// already swallowed below this layer, so a Drive error means the local
// integration status was not updated (still active): it is returned before
// anything is cancelled.
func (s *Service) Disconnect(ctx context.Context, user UserID) error {
	if err := s.drive.Disconnect(ctx, user); err != nil {
		return fmt.Errorf("disconnect drive: %w", err)
	}
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	if err := s.repo.ResetPendingUploads(ctx, user); err != nil {
		return fmt.Errorf("reset pending uploads: %w", err)
	}
	return nil
}

// DeleteAllData revokes Drive access, cancels all pending jobs and deletes all of
// the user's data, then soft-deletes the user. A failing revoke does not block
// the deletion: it is reported afterwards as ErrRevokeIncomplete, which callers
// treat as success.
func (s *Service) DeleteAllData(ctx context.Context, user UserID) error {
	revokeErr := s.drive.Revoke(ctx, user)
	if err := s.uploads.CancelUserJobs(ctx, user); err != nil {
		return fmt.Errorf("cancel jobs: %w", err)
	}
	if err := s.repo.DeleteUserData(ctx, user); err != nil {
		return fmt.Errorf("delete user data: %w", err)
	}
	if revokeErr != nil {
		return fmt.Errorf("revoke drive access: %w: %w", ErrRevokeIncomplete, revokeErr)
	}
	return nil
}

// RetryFailed re-queues every failed Drive upload under one transfer status
// message that takes over the given Telegram message. ok is false when nothing failed.
func (s *Service) RetryFailed(ctx context.Context, user UserID, chatID int64, messageID int) (StatusView, bool, error) {
	ids, err := s.repo.ClaimAllFailed(ctx, user)
	if err != nil {
		return StatusView{}, false, fmt.Errorf("claim failed: %w", err)
	}
	return s.startGroupedUpload(ctx, user, chatID, messageID, ids)
}

// ExportAll returns the user's saved documents (capped at exportLimit) to resend.
func (s *Service) ExportAll(ctx context.Context, user UserID) (Export, error) {
	total, err := s.repo.CountSaved(ctx, user)
	if err != nil {
		return Export{}, fmt.Errorf("count saved: %w", err)
	}
	docs, err := s.repo.SavedForExport(ctx, user, exportLimit)
	if err != nil {
		return Export{}, fmt.Errorf("list for export: %w", err)
	}
	return Export{Docs: docs, Total: total, Truncated: total > exportLimit}, nil
}

// Card returns one document's detail, scoped to the user.
func (s *Service) Card(ctx context.Context, user UserID, id DocumentID) (Card, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Card{}, fmt.Errorf("load document: %w", err)
	}
	if doc.InDrive() {
		// The file may sit in an older category folder (it is not moved on
		// recategorize), so link only to the file itself.
		return Card{Doc: doc}, nil
	}
	folderID, _, err := s.folders.CategoryFolderID(ctx, user, doc.Category)
	if err != nil {
		return Card{}, fmt.Errorf("category folder: %w", err)
	}
	return Card{Doc: doc, CategoryFolderID: folderID}, nil
}

// OpenCard is Card for a card the user opens from a list or search: when the
// document has a live Drive file it also checks, at most once a minute and within
// remoteCheckTimeout, that the file still exists, and flips the sync status
// ok <-> missing accordingly. A failure of the check itself (network, timeout,
// revoked or disconnected Drive) is not an error: the card stays as it is. A
// database error comes back together with the card read so far (Card.Doc.ID set),
// which the caller may still show; nothing is re-uploaded here.
func (s *Service) OpenCard(ctx context.Context, user UserID, id DocumentID) (Card, error) {
	card, err := s.Card(ctx, user, id)
	if err != nil {
		return Card{}, err
	}
	doc := card.Doc
	if !doc.InDrive() || doc.DriveFileID == "" || (doc.SyncStatus != SyncOK && doc.SyncStatus != SyncMissing) {
		return card, nil
	}
	if !s.allowRemoteCheck(doc.ID) {
		return card, nil
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return card, fmt.Errorf("drive status: %w", err)
	}
	if !connected {
		return card, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, remoteCheckTimeout)
	defer cancel()
	exists, err := s.drive.FileExists(checkCtx, user, doc.DriveFileID)
	if err != nil {
		return card, nil
	}

	from, to := SyncOK, SyncMissing
	if exists {
		from, to = SyncMissing, SyncOK
	}
	if doc.SyncStatus != from {
		return card, nil
	}
	moved, err := s.repo.TransitionSyncStatus(ctx, user, doc.ID, doc.DriveFileID, from, to)
	if err != nil {
		return card, fmt.Errorf("transition sync status: %w", err)
	}
	if !moved {
		return card, nil
	}
	card.Doc.SyncStatus, card.Doc.SyncReason = to, ""
	return card, nil
}

// allowRemoteCheck reports whether the document's Drive file may be checked now,
// and records the attempt. Entries older than the throttle window are dropped on
// the way, so the map does not grow without bound.
func (s *Service) allowRemoteCheck(id DocumentID) bool {
	now := s.now()
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	for k, t := range s.checks {
		if now.Sub(t) >= remoteCheckEvery {
			delete(s.checks, k)
		}
	}
	if _, recent := s.checks[id]; recent {
		return false
	}
	s.checks[id] = now
	return true
}

// Rename changes a document's title to a normalized, non-blank value of at most
// MaxTitleRunes runes: a longer title is refused (ErrTitleTooLong), not cut. When
// Drive is connected a backed-up document's file is renamed by the sync worker.
func (s *Service) Rename(ctx context.Context, user UserID, id DocumentID, title string) (Card, error) {
	title = collapseTitle(title)
	if title == "" {
		return Card{}, ErrEmptyTitle
	}
	if len([]rune(title)) > MaxTitleRunes {
		return Card{}, ErrTitleTooLong
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Card{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		_, err = s.syncs.SetTitle(ctx, user, id, title)
	} else {
		err = s.repo.SetTitle(ctx, user, id, title)
	}
	if err != nil {
		return Card{}, fmt.Errorf("rename document: %w", err)
	}
	return s.Card(ctx, user, id)
}

// Recategorize moves a document to another category. When Drive is connected a
// backed-up document's file is moved by the sync worker; otherwise it stays in
// its old Drive folder and the card says so.
func (s *Service) Recategorize(ctx context.Context, user UserID, id DocumentID, cat Category) (Card, error) {
	if !cat.Valid() {
		return Card{}, ErrUnknownCategory
	}
	old, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Card{}, fmt.Errorf("load document: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Card{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		_, err = s.syncs.SetCategory(ctx, user, id, cat)
	} else {
		err = s.repo.SetCategory(ctx, user, id, cat)
	}
	if err != nil {
		return Card{}, fmt.Errorf("recategorize document: %w", err)
	}
	card, err := s.Card(ctx, user, id)
	if err != nil {
		return Card{}, err
	}
	// Without a connected cloud nothing moves the file: it stays in its old folder.
	// The card links to the file itself, and the new category's folder would mislead.
	if !connected && card.Doc.InDrive() && old.Category != cat {
		card.FileInOldFolder = true
		card.CategoryFolderID = ""
	}
	return card, nil
}

// Delete removes a document, scoped to the user. A backed-up document's cloud file
// is moved to the trash by a job queued with the row deletion; when Drive is not
// connected the file stays and the result says so.
func (s *Service) Delete(ctx context.Context, user UserID, id DocumentID) (Deleted, error) {
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Deleted{}, fmt.Errorf("drive status: %w", err)
	}
	// Whether the Drive file is trashed is decided on the deleted row itself.
	kept, err := s.syncs.DeleteDocument(ctx, user, id, connected)
	if err != nil {
		return Deleted{}, fmt.Errorf("delete document: %w", err)
	}
	return Deleted{RemoteKept: kept}, nil
}

// Catalog lists the user's categories with counts. The 📥 banner (NotInDrive)
// is only meaningful once Drive is connected.
func (s *Service) Catalog(ctx context.Context, user UserID) (Catalog, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return Catalog{}, fmt.Errorf("category counts: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Catalog{}, fmt.Errorf("drive status: %w", err)
	}
	var notInDrive int
	if connected {
		if notInDrive, err = s.repo.CountNotBackedUp(ctx, user); err != nil {
			return Catalog{}, fmt.Errorf("count not backed up: %w", err)
		}
	}
	return Catalog{Counts: foldCategoryCounts(counts), NotInDrive: notInDrive, DriveConnected: connected}, nil
}

// CategoryPage returns one page (pageSize) of a category, newest first. page is
// 1-based and clamped into range.
func (s *Service) CategoryPage(ctx context.Context, user UserID, cat Category, page int) (CategoryPage, error) {
	if !cat.Valid() {
		return CategoryPage{}, ErrUnknownCategory
	}
	total, err := s.categoryTotal(ctx, user, cat)
	if err != nil {
		return CategoryPage{}, err
	}
	pages := (total + pageSize - 1) / pageSize
	if pages == 0 {
		pages = 1
	}
	page = clamp(page, 1, pages)

	docs, err := s.repo.ListByCategory(ctx, user, cat, pageSize, (page-1)*pageSize)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("list by category: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("drive status: %w", err)
	}
	folderID, _, err := s.folders.CategoryFolderID(ctx, user, cat)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("category folder: %w", err)
	}
	return CategoryPage{
		Category: cat, Docs: docs, Page: page, Pages: pages, Total: total, DriveConnected: connected,
		FolderID: folderID,
	}, nil
}

// Search finds up to searchLimit saved documents matching query. The query is
// trimmed and cut to maxQueryRunes; a blank one is not searched (BlankQuery).
func (s *Service) Search(ctx context.Context, user UserID, query string) (Found, error) {
	query = normalizeQuery(query)
	if query == "" {
		return Found{BlankQuery: true}, nil
	}
	docs, err := s.repo.Search(ctx, user, query, searchLimit)
	if err != nil {
		return Found{}, fmt.Errorf("search documents: %w", err)
	}
	return Found{Query: query, Docs: docs}, nil
}

// normalizeQuery trims a search query and cuts it to maxQueryRunes.
func normalizeQuery(query string) string {
	query = strings.TrimSpace(query)
	if r := []rune(query); len(r) > maxQueryRunes {
		query = strings.TrimSpace(string(r[:maxQueryRunes]))
	}
	return query
}

// categoryTotal reads the saved-document count for one category.
func (s *Service) categoryTotal(ctx context.Context, user UserID, cat Category) (int, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("category counts: %w", err)
	}
	for _, c := range foldCategoryCounts(counts) {
		if c.Category == cat {
			return c.Count, nil
		}
	}
	return 0, nil
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

// defaultLocation is the zone for default-title dates: Asia/Almaty, or a fixed
// UTC+5 if the zone database is unavailable.
func defaultLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Almaty"); err == nil {
		return loc
	}
	return time.FixedZone("UTC+5", 5*60*60)
}
