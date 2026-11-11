package docsbot

import "time"

// UserID is a user's internal (UUID) identifier.
type UserID string

// DocumentID is a document's internal (UUID) identifier.
type DocumentID string

// StatusMessageID is a batch status message's internal (UUID) identifier.
type StatusMessageID string

// Status is the document's own lifecycle, independent of Drive.
type Status string

// A document is pending until its category is chosen, then saved.
const (
	StatusPending Status = "pending"
	StatusSaved   Status = "saved"
)

// BackupStatus tracks the Drive upload, separate from Status. It stays none
// until Drive is connected.
type BackupStatus string

// Drive upload lifecycle (model.md).
const (
	BackupNone      BackupStatus = "none"
	BackupPending   BackupStatus = "pending"
	BackupUploading BackupStatus = "uploading"
	BackupBackedUp  BackupStatus = "backed_up"
	BackupFailed    BackupStatus = "failed"
	// BackupTooLarge marks a file above the Telegram download limit; it stays in Telegram.
	BackupTooLarge BackupStatus = "too_large"
)

// Terminal reports whether the upload has reached a final state: backed up,
// failed or too large.
func (b BackupStatus) Terminal() bool {
	return b == BackupBackedUp || b == BackupFailed || b == BackupTooLarge
}

// SyncStatus tracks whether the Drive file matches the document's title, category
// and content. It only matters for a backed-up document.
type SyncStatus string

// Cloud sync lifecycle.
const (
	SyncOK      SyncStatus = "ok"
	SyncPending SyncStatus = "pending"
	SyncFailed  SyncStatus = "failed"
	// SyncMissing means the Drive file was deleted outside the bot.
	SyncMissing SyncStatus = "missing"
)

// SyncReasonRevoked is the SyncReason of a sync that failed because Drive access was revoked.
const SyncReasonRevoked = "revoked"

// Kind records how the file was sent, so it can be resent with the right method.
type Kind string

// How a file arrived.
const (
	KindPhoto    Kind = "photo"
	KindDocument Kind = "document"
)

// Document is a file the user sent, stored by its Telegram file_id. Until Drive
// is connected it lives only in Telegram (BackupStatus none).
type Document struct {
	ID             DocumentID
	UserID         UserID
	Status         Status
	Category       Category
	Title          string
	Kind           Kind
	TgFileID       string
	TgFileUniqueID string
	DriveFileID    string
	// FileSize is the size in bytes, 0 if unknown.
	FileSize int64
	// PageCount is the number of pages of a PDF, 0 if unknown.
	PageCount int
	// Width and Height are the pixel size of an image, 0 if unknown.
	Width, Height int
	MIME          string
	BackupStatus  BackupStatus
	// SyncStatus is the state of the Drive copy relative to the row; SyncReason
	// explains a failed sync ("" or SyncReasonRevoked).
	SyncStatus SyncStatus
	SyncReason string
	// MetaVersion counts title, category and content changes.
	MetaVersion int
	// ContentDirty means the Drive file content must be replaced.
	ContentDirty bool
	// MediaGroupID is the Telegram album the file arrived in, "" for a single file.
	MediaGroupID string
	// StatusMessageID is the status-message group the document belongs to, "" if none.
	StatusMessageID StatusMessageID
	CreatedAt       time.Time
}

// StatusMessage is the single Telegram message that tracks a batch of uploads.
type StatusMessage struct {
	ID        StatusMessageID
	UserID    UserID
	ChatID    int64
	MessageID int
	CreatedAt time.Time
	Closed    bool
	Kind      StatusKind
}

// StatusKind distinguishes how a status message renders.
type StatusKind string

// Status kinds.
const (
	StatusSingle   StatusKind = "single"   // one uploading document, rendered as a live card
	StatusBatch    StatusKind = "batch"    // 2+ documents, rendered as a counter + lines
	StatusTransfer StatusKind = "transfer" // a bulk transfer/retry, rendered like a batch with its own header
)

// InDrive reports whether the document has been backed up to the user's Drive.
func (d Document) InDrive() bool { return d.BackupStatus == BackupBackedUp }

// IncomingFile is a file just received from Telegram, before it is stored.
type IncomingFile struct {
	UserID       UserID
	Kind         Kind
	FileID       string
	FileUniqueID string
	Title        string
	// FileSize is the size in bytes, 0 if unknown.
	FileSize int64
	MIME     string
	// PageCount is the number of pages of a PDF, 0 if unknown.
	PageCount int
	// Width and Height are the pixel size of an image, 0 if unknown.
	Width, Height int
	// MediaGroupID is the Telegram album the file belongs to, "" for a single file.
	MediaGroupID string
}

// IntakeStats is what the intake guards need to know about a user, fetched in
// one query: the pending backlog, files created in the flood window, and the total.
type IntakeStats struct {
	Pending int
	Recent  int
	Total   int
}
