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
}

// Option tunes a Service at construction.
type Option func(*Service)

// WithClock replaces the time source; used to pin the status-message window in tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService wires the service around its ports.
func NewService(repo Repo, drive Drive, files FileSource, uploads Uploads, folders Folders, status StatusMessages, opts ...Option) *Service {
	s := &Service{repo: repo, drive: drive, files: files, uploads: uploads, folders: folders, status: status, now: time.Now}
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

	doc, err := s.repo.CreateDocument(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return NeedCategory{Doc: doc}, nil
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

	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
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

// BackupResult is the outcome of a Drive upload attempt.
type BackupResult struct {
	Doc      Document
	TooLarge bool
}

// Backup uploads a saved document to the user's Drive. It is the upload_document
// worker's use case and is idempotent: an already backed-up document is a no-op.
// A file above the download limit is marked too_large and left in Telegram; a
// revoked grant returns ErrDriveRevoked; transient errors are returned for retry.
func (s *Service) Backup(ctx context.Context, user UserID, id DocumentID) (BackupResult, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return BackupResult{}, fmt.Errorf("load document: %w", err)
	}
	if doc.BackupStatus == BackupBackedUp {
		return BackupResult{Doc: doc}, nil
	}
	if err := s.repo.SetBackupStatus(ctx, id, BackupUploading); err != nil {
		return BackupResult{}, fmt.Errorf("mark uploading: %w", err)
	}

	data, mime, err := s.files.Download(ctx, doc.TgFileID)
	if errors.Is(err, ErrFileTooLarge) {
		if err := s.repo.SetBackupStatus(ctx, id, BackupTooLarge); err != nil {
			return BackupResult{}, fmt.Errorf("mark too large: %w", err)
		}
		doc.BackupStatus = BackupTooLarge
		return BackupResult{Doc: doc, TooLarge: true}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("download file: %w", err)
	}

	file, err := s.drive.Upload(ctx, user, DriveUpload{Category: doc.Category, Name: doc.Title, MIME: mime, Data: data})
	if errors.Is(err, ErrDriveRevoked) {
		return BackupResult{}, ErrDriveRevoked
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("upload to drive: %w", err)
	}

	if err := s.repo.MarkBackedUp(ctx, id, file.ID); err != nil {
		return BackupResult{}, fmt.Errorf("mark backed up: %w", err)
	}
	doc.BackupStatus = BackupBackedUp
	doc.DriveFileID = file.ID
	return BackupResult{Doc: doc}, nil
}

// MarkBackupFailed records a terminal upload failure. The user is accepted for a
// consistent, user-scoped API even though the status update is keyed by id.
func (s *Service) MarkBackupFailed(ctx context.Context, _ UserID, id DocumentID) error {
	if err := s.repo.SetBackupStatus(ctx, id, BackupFailed); err != nil {
		return fmt.Errorf("mark backup failed: %w", err)
	}
	return nil
}

// TransferSaved enqueues a Drive upload for every saved document not yet backed
// up, returning how many were queued. Used after the user connects Drive.
func (s *Service) TransferSaved(ctx context.Context, user UserID) (int, error) {
	ids, err := s.repo.NotBackedUpIDs(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("list not backed up: %w", err)
	}
	for i, id := range ids {
		if err := s.uploads.EnqueueUpload(ctx, user, id); err != nil {
			return i, fmt.Errorf("enqueue upload: %w", err)
		}
	}
	return len(ids), nil
}

// NotBackedUpCount reports how many saved documents are not yet on Drive.
func (s *Service) NotBackedUpCount(ctx context.Context, user UserID) (int, error) {
	n, err := s.repo.CountNotBackedUp(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("count not backed up: %w", err)
	}
	return n, nil
}

// DriveConnected reports whether the user's Drive is connected.
func (s *Service) DriveConnected(ctx context.Context, user UserID) (bool, error) {
	ok, err := s.drive.Connected(ctx, user)
	if err != nil {
		return false, fmt.Errorf("drive status: %w", err)
	}
	return ok, nil
}

// RetryBackup re-enqueues a single document's Drive upload.
func (s *Service) RetryBackup(ctx context.Context, user UserID, id DocumentID) error {
	if err := s.uploads.EnqueueUpload(ctx, user, id); err != nil {
		return fmt.Errorf("enqueue upload: %w", err)
	}
	return nil
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

// Disconnect revokes Drive access (best-effort) and cancels the user's pending uploads.
func (s *Service) Disconnect(ctx context.Context, user UserID) error {
	if err := s.drive.Revoke(ctx, user); err != nil {
		return fmt.Errorf("revoke drive access: %w", err)
	}
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	return nil
}

// DeleteAllData revokes Drive access, cancels pending uploads and deletes all of the
// user's data, then soft-deletes the user.
func (s *Service) DeleteAllData(ctx context.Context, user UserID) error {
	if err := s.drive.Revoke(ctx, user); err != nil {
		return fmt.Errorf("revoke drive access: %w", err)
	}
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	if err := s.repo.DeleteUserData(ctx, user); err != nil {
		return fmt.Errorf("delete user data: %w", err)
	}
	return nil
}

// RetryFailed re-enqueues a Drive upload for every failed document, returning the count.
func (s *Service) RetryFailed(ctx context.Context, user UserID) (int, error) {
	ids, err := s.repo.FailedIDs(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("list failed: %w", err)
	}
	for i, id := range ids {
		if err := s.uploads.EnqueueUpload(ctx, user, id); err != nil {
			return i, fmt.Errorf("enqueue upload: %w", err)
		}
	}
	return len(ids), nil
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

// Search finds up to searchLimit saved documents matching query.
func (s *Service) Search(ctx context.Context, user UserID, query string) (Found, error) {
	query = strings.TrimSpace(query)
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
