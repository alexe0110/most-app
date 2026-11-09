package docsbot

import (
	"context"
	"time"
)

// Repo stores documents. Every method is scoped by user id, so one user can
// never reach another's documents. Declared here, at the consumer; implemented
// by docsbot/postgres.
type Repo interface {
	// CreateDocument stores a received file as pending (no category yet).
	CreateDocument(ctx context.Context, in IncomingFile) (Document, error)
	// DocumentByFileUniqueID returns the user's document for this Telegram
	// file, or ErrNotFound.
	DocumentByFileUniqueID(ctx context.Context, user UserID, uniqueID string) (Document, error)
	// DocumentByID returns the user's document, or ErrNotFound.
	DocumentByID(ctx context.Context, user UserID, id DocumentID) (Document, error)
	// PendingByMediaGroup lists the user's still-pending documents of one album, oldest first.
	PendingByMediaGroup(ctx context.Context, user UserID, group string) ([]Document, error)
	// IntakeStats reports the user's pending count, files created after since, and total, in one query.
	IntakeStats(ctx context.Context, user UserID, since time.Time) (IntakeStats, error)
	// PurgeStalePending deletes every user's pending documents created before
	// olderThan and returns how many were removed.
	PurgeStalePending(ctx context.Context, olderThan time.Time) (int, error)
	// SaveDocument sets the category and marks the document saved.
	SaveDocument(ctx context.Context, user UserID, id DocumentID, cat Category) error
	SetBackupStatus(ctx context.Context, user UserID, id DocumentID, s BackupStatus) error
	// ClaimFailed atomically moves a failed document to pending. It reports
	// false when the document is not failed or is not the user's.
	ClaimFailed(ctx context.Context, user UserID, id DocumentID) (bool, error)
	// MarkUploading sets backup_status=uploading WITHOUT touching updated_at, so
	// the stuck-clock keeps running from enqueue time (sweeper 24h backstop).
	MarkUploading(ctx context.Context, user UserID, id DocumentID) error
	// ResetPendingUploads resets the user's pending/uploading saved documents back to none.
	ResetPendingUploads(ctx context.Context, user UserID) error
	// SetTitle renames the document; ErrNotFound if the user has no such document.
	SetTitle(ctx context.Context, user UserID, id DocumentID, title string) error
	// TitleExistsInCategory reports whether the user already has a saved document
	// with this exact title in the category (used to de-duplicate default names).
	TitleExistsInCategory(ctx context.Context, user UserID, cat Category, title string) (bool, error)
	// SetCategory recategorizes the document; ErrNotFound if the user has no such document.
	SetCategory(ctx context.Context, user UserID, id DocumentID, cat Category) error
	// DeleteDocument removes the document; ErrNotFound if the user has no such document.
	DeleteDocument(ctx context.Context, user UserID, id DocumentID) error
	// CountSaved counts the user's saved documents.
	CountSaved(ctx context.Context, user UserID) (int, error)
	// CountNotBackedUp counts saved documents not yet on Drive.
	CountNotBackedUp(ctx context.Context, user UserID) (int, error)
	CategoryCounts(ctx context.Context, user UserID) ([]CategoryCount, error)
	ListByCategory(ctx context.Context, user UserID, cat Category, limit, offset int) ([]Document, error)
	Search(ctx context.Context, user UserID, query string, limit int) ([]Document, error)
	// MarkBackedUp records a successful Drive upload: backup_status=backed_up + drive_file_id.
	MarkBackedUp(ctx context.Context, user UserID, id DocumentID, driveFileID string) error
	// ClaimTransferable atomically moves every saved document that is not on Drive
	// (none or failed) to pending and returns their ids, oldest first. Concurrent
	// callers never receive the same document.
	ClaimTransferable(ctx context.Context, user UserID) ([]DocumentID, error)
	// BackupStatusCounts returns the user's saved-document counts grouped by backup_status.
	BackupStatusCounts(ctx context.Context, user UserID) ([]BackupStatusCount, error)
	// ClaimAllFailed atomically moves every failed upload to pending and returns
	// their ids, oldest first.
	ClaimAllFailed(ctx context.Context, user UserID) ([]DocumentID, error)
	// SavedForExport lists up to limit of the user's saved documents, oldest first, to resend from Telegram.
	SavedForExport(ctx context.Context, user UserID, limit int) ([]Document, error)
	// StuckUploads lists saved documents stuck in pending/uploading since before
	// olderThan, for users with an active Drive integration, oldest first.
	StuckUploads(ctx context.Context, olderThan time.Time, limit int) ([]StuckUpload, error)
	// DeleteUserData deletes the user's documents, drive_folders and integrations and
	// soft-deletes the user (users.deleted_at), all in one transaction.
	DeleteUserData(ctx context.Context, user UserID) error
	// DeleteAllFolders clears the user's cached Drive folder ids.
	DeleteAllFolders(ctx context.Context, user UserID) error
}

// StuckUpload is a saved document stuck mid-upload (pending/uploading) for a
// user whose Drive is connected.
type StuckUpload struct {
	User      UserID
	ID        DocumentID
	UpdatedAt time.Time
}

// CategoryCount is the number of saved documents in a category.
type CategoryCount struct {
	Category Category
	Count    int
}

// BackupStatusCount is the number of saved documents in one backup_status.
type BackupStatusCount struct {
	Status BackupStatus
	Count  int
}

// Drive reports a user's Google Drive connection and stores files there.
type Drive interface {
	Connected(ctx context.Context, user UserID) (bool, error)
	// Upload stores the document's bytes on the user's Drive. A revoked grant
	// returns ErrDriveRevoked.
	Upload(ctx context.Context, user UserID, in DriveUpload) (DriveFile, error)
	// Revoke best-effort revokes the user's Drive access at Google and marks the
	// integration revoked locally. The adapter guarantees the local revoke even if
	// Google is unreachable; an error means the local state could not be updated.
	Revoke(ctx context.Context, user UserID) error
	// Disconnect is a user-initiated disconnect: it marks the integration
	// disconnected-by-user, so later uploads fall back to Telegram silently.
	Disconnect(ctx context.Context, user UserID) error
}

// DriveUpload is a file to copy to the user's Drive.
type DriveUpload struct {
	Category Category
	Name     string
	MIME     string
	Data     []byte
	DocID    string // stable document id, written to Drive appProperties for idempotent re-upload
}

// DriveFile is a stored Drive file: its id and a shareable link.
type DriveFile struct {
	ID   string
	Link string
}

// FileSource downloads a stored Telegram file by its file_id. A file above the
// Bot API limit returns ErrFileTooLarge.
type FileSource interface {
	Download(ctx context.Context, fileID string) (data []byte, mime string, err error)
}

// Folders reports the cached Drive folder id for a user's category, if known.
type Folders interface {
	CategoryFolderID(ctx context.Context, user UserID, cat Category) (id string, ok bool, err error)
}

// Uploads enqueues a document's Drive upload, marking backup_status=pending and
// inserting the job in one transaction.
type Uploads interface {
	EnqueueUpload(ctx context.Context, user UserID, id DocumentID) error
	// RequeueUpload inserts the upload job only, unique, without touching
	// backup_status or updated_at; a live job dedups to a no-op.
	RequeueUpload(ctx context.Context, user UserID, id DocumentID) error
	// CancelUploads removes the user's pending (not yet running) upload_document jobs.
	CancelUploads(ctx context.Context, user UserID) error
}

// StatusMessages persists the per-batch upload status message and the documents
// attached to it. Declared by the domain, implemented by docsbot/postgres.
type StatusMessages interface {
	// OpenForUser returns the user's newest unclosed status message, if any.
	OpenForUser(ctx context.Context, user UserID) (StatusMessage, bool, error)
	Create(ctx context.Context, user UserID, chatID int64, messageID int, kind StatusKind) (StatusMessage, error)
	Close(ctx context.Context, user UserID, id StatusMessageID) error
	SetMessageID(ctx context.Context, user UserID, id StatusMessageID, messageID int) error
	Attach(ctx context.Context, user UserID, doc DocumentID, id StatusMessageID) error
	DocumentsIn(ctx context.Context, user UserID, id StatusMessageID) ([]Document, error)
	// ByID returns the user's status message, or ErrNotFound (also for another user's).
	ByID(ctx context.Context, user UserID, id StatusMessageID) (StatusMessage, error)
}
