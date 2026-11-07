package docsbot

import "errors"

// Domain errors. Adapters translate their own errors into these (e.g.
// pgx.ErrNoRows → ErrNotFound); the channel adapter decides what to show.
var (
	// ErrNotFound means the user has no such document.
	ErrNotFound = errors.New("document not found")
	// ErrUnknownCategory means the category is not one of AllCategories.
	ErrUnknownCategory = errors.New("unknown category")
	// ErrEmptyTitle means a rename was given a blank title.
	ErrEmptyTitle = errors.New("empty title")
	// ErrFileTooLarge means the file is above Telegram's download limit, so it cannot be backed up to Drive.
	ErrFileTooLarge = errors.New("file too large for drive")
	// ErrDriveRevoked means the user's Drive access was revoked; they must reconnect.
	ErrDriveRevoked = errors.New("drive access revoked")
)
