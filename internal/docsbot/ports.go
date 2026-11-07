package docsbot

import "context"

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
	// SaveDocument sets the category and marks the document saved.
	SaveDocument(ctx context.Context, user UserID, id DocumentID, cat Category) error
	SetBackupStatus(ctx context.Context, id DocumentID, s BackupStatus) error
	SetTitle(ctx context.Context, user UserID, id DocumentID, title string) error
	SetCategory(ctx context.Context, user UserID, id DocumentID, cat Category) error
	DeleteDocument(ctx context.Context, user UserID, id DocumentID) error
	// CountSaved counts the user's saved documents.
	CountSaved(ctx context.Context, user UserID) (int, error)
	// CountNotBackedUp counts saved documents not yet on Drive.
	CountNotBackedUp(ctx context.Context, user UserID) (int, error)
	CategoryCounts(ctx context.Context, user UserID) ([]CategoryCount, error)
	ListByCategory(ctx context.Context, user UserID, cat Category, limit, offset int) ([]Document, error)
	Search(ctx context.Context, user UserID, query string, limit int) ([]Document, error)
}

// CategoryCount is the number of saved documents in a category.
type CategoryCount struct {
	Category Category
	Count    int
}

// Drive reports a user's Google Drive connection. #11 adds the real
// implementation (over the integrations table) plus enqueuing uploads; for now
// a wiring stub reports "not connected", so documents stay in Telegram.
type Drive interface {
	Connected(ctx context.Context, user UserID) (bool, error)
}
