package docsbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// maxDocumentsPerUser caps one user's total documents.
	maxDocumentsPerUser = 5000
	// intakeWindow is the flood-check window.
	intakeWindow = time.Minute
	// pendingTTL is how long a document may wait for a category before it is purged.
	pendingTTL = 24 * time.Hour
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
	now     func() time.Time
	name    func(Category) string
}

// Option tunes a Service at construction.
type Option func(*Service)

// WithClock replaces the time source; used to pin the status-message window in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithCategoryNamer sets how a category slug is turned into the display name
// used in a document's default title. Injected from the telegram adapter.
func WithCategoryNamer(fn func(Category) string) Option {
	return func(s *Service) { s.name = fn }
}

// NewService wires the service around its ports.
func NewService(repo Repo, drive Drive, files FileSource, uploads Uploads, folders Folders, status StatusMessages, opts ...Option) *Service {
	s := &Service{
		repo: repo, drive: drive, files: files, uploads: uploads, folders: folders, status: status,
		now:  time.Now,
		name: func(c Category) string { return string(c) },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Receive stores a freshly sent file as pending and asks for a category. A file
// already saved returns AlreadySaved; a still-pending one re-asks the category.
func (s *Service) Receive(ctx context.Context, in IncomingFile) (Reply, error) {
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
	case stats.Total >= maxDocumentsPerUser:
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
// ChooseCategory: once nothing is pending the already-saved document is returned.
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
		out.Docs = append(out.Docs, saved.Doc)
		out.OfferDrive = out.OfferDrive || saved.OfferDrive
	}
	return out, nil
}

// ChooseCategory saves a pending document under cat. It is idempotent: a second
// tap on an already-saved document just returns it. When Drive is connected the
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
		return Saved{Doc: doc}, nil
	}

	if err := s.repo.SaveDocument(ctx, user, id, cat); err != nil {
		return Saved{}, fmt.Errorf("save document: %w", err)
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
	base := s.name(cat) + " — " + s.now().Format("02.01.2006")
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
}

// Backup uploads a saved document to the user's Drive. It is the upload_document
// worker's use case and is idempotent: an already backed-up document is a no-op.
// A file above the download limit is marked too_large and left in Telegram; a
// revoked grant returns ErrDriveRevoked; transient errors are returned for retry.
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

	file, err := s.drive.Upload(ctx, user, DriveUpload{Category: doc.Category, Name: doc.Title, MIME: mime, Data: data, DocID: string(id)})
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
	if err != nil {
		return BackupResult{}, fmt.Errorf("upload to drive: %w", err)
	}

	if err := s.repo.MarkBackedUp(ctx, user, id, file.ID); err != nil {
		return BackupResult{}, fmt.Errorf("mark backed up: %w", err)
	}
	doc.BackupStatus = BackupBackedUp
	doc.DriveFileID = file.ID
	return BackupResult{Doc: doc}, nil
}

// MarkBackupFailed records a terminal upload failure.
func (s *Service) MarkBackupFailed(ctx context.Context, user UserID, id DocumentID) error {
	if err := s.repo.SetBackupStatus(ctx, user, id, BackupFailed); err != nil {
		return fmt.Errorf("mark backup failed: %w", err)
	}
	return nil
}

// SweepResult summarizes one sweeper run.
type SweepResult struct {
	Found    int
	Requeued int
	Failed   []StuckUpload
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
			if err := s.repo.SetBackupStatus(ctx, r.User, r.ID, BackupFailed); err != nil {
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
	view := SettingsView{DriveConnected: connected}
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
// uploads and resets stuck documents back to Telegram-only. A failing Drive
// disconnect does not stop the local work: it is reported afterwards as
// ErrRevokeIncomplete, which callers treat as success.
func (s *Service) Disconnect(ctx context.Context, user UserID) error {
	driveErr := s.drive.Disconnect(ctx, user)
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	if err := s.repo.ResetPendingUploads(ctx, user); err != nil {
		return fmt.Errorf("reset pending uploads: %w", err)
	}
	if driveErr != nil {
		return fmt.Errorf("disconnect drive: %w: %w", ErrRevokeIncomplete, driveErr)
	}
	return nil
}

// DeleteAllData revokes Drive access, cancels pending uploads and deletes all of
// the user's data, then soft-deletes the user. A failing revoke does not block
// the deletion: it is reported afterwards as ErrRevokeIncomplete, which callers
// treat as success.
func (s *Service) DeleteAllData(ctx context.Context, user UserID) error {
	revokeErr := s.drive.Revoke(ctx, user)
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
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
	folderID, _, err := s.folders.CategoryFolderID(ctx, user, doc.Category)
	if err != nil {
		return Card{}, fmt.Errorf("category folder: %w", err)
	}
	return Card{Doc: doc, CategoryFolderID: folderID}, nil
}

// Rename changes a document's title to a non-blank value.
func (s *Service) Rename(ctx context.Context, user UserID, id DocumentID, title string) (Card, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Card{}, ErrEmptyTitle
	}
	if err := s.repo.SetTitle(ctx, user, id, title); err != nil {
		return Card{}, fmt.Errorf("rename document: %w", err)
	}
	return s.Card(ctx, user, id)
}

// Recategorize moves a document to another category.
func (s *Service) Recategorize(ctx context.Context, user UserID, id DocumentID, cat Category) (Card, error) {
	if !cat.Valid() {
		return Card{}, ErrUnknownCategory
	}
	if err := s.repo.SetCategory(ctx, user, id, cat); err != nil {
		return Card{}, fmt.Errorf("recategorize document: %w", err)
	}
	// TODO(#11): if backed up, move the file to the new Drive folder.
	return s.Card(ctx, user, id)
}

// Delete removes a document, scoped to the user.
func (s *Service) Delete(ctx context.Context, user UserID, id DocumentID) error {
	// TODO(#11): if backed up, delete the file on Drive too.
	if err := s.repo.DeleteDocument(ctx, user, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
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
	return Catalog{Counts: counts, NotInDrive: notInDrive, DriveConnected: connected}, nil
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
	query = strings.TrimSpace(query)
	if r := []rune(query); len(r) > maxQueryRunes {
		query = strings.TrimSpace(string(r[:maxQueryRunes]))
	}
	if query == "" {
		return Found{BlankQuery: true}, nil
	}
	docs, err := s.repo.Search(ctx, user, query, searchLimit)
	if err != nil {
		return Found{}, fmt.Errorf("search documents: %w", err)
	}
	return Found{Query: query, Docs: docs}, nil
}

// categoryTotal reads the saved-document count for one category.
func (s *Service) categoryTotal(ctx context.Context, user UserID, cat Category) (int, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("category counts: %w", err)
	}
	for _, c := range counts {
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
