package docsbot

import "time"

// UserID is a user's internal (UUID) identifier.
type UserID string

// DocumentID is a document's internal (UUID) identifier.
type DocumentID string

// StatusMessageID is a batch status message's internal (UUID) identifier.
type StatusMessageID string

// Category is a document bucket. The stored value is a stable slug; the Russian
// label shown to the user lives in the telegram adapter (texts.go).
type Category string

// MVP categories (product.md).
const (
	CatPassport  Category = "passport"
	CatContracts Category = "contracts"
	CatReceipts  Category = "receipts"
	CatAuto      Category = "auto"
	CatMedical   Category = "medical"
	CatEduWork   Category = "edu_work"
	CatOther     Category = "other"
)

// AllCategories lists the categories in display order (product.md).
var AllCategories = []Category{
	CatPassport, CatContracts, CatReceipts, CatAuto, CatMedical, CatEduWork, CatOther,
}

// Valid reports whether c is one of the known categories.
func (c Category) Valid() bool {
	for _, k := range AllCategories {
		if c == k {
			return true
		}
	}
	return false
}

// ParseCategory turns a stored or callback string into a Category.
func ParseCategory(s string) (Category, bool) {
	c := Category(s)
	return c, c.Valid()
}

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
	BackupStatus   BackupStatus
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
}

// InDrive reports whether the document has been backed up to the user's Drive.
func (d Document) InDrive() bool { return d.BackupStatus == BackupBackedUp }

// IncomingFile is a file just received from Telegram, before it is stored.
type IncomingFile struct {
	UserID       UserID
	Kind         Kind
	FileID       string
	FileUniqueID string
	Title        string
}
