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
	// ErrDriveDisconnected means the user turned Drive off themselves; the document
	// falls back to Telegram-only: no failure, no reconnect prompt.
	ErrDriveDisconnected = errors.New("drive access disconnected by user")
	// ErrNotRetryable means a retry was asked for a document that is not in the
	// failed state (already retried, in flight, done) or is not the user's.
	ErrNotRetryable = errors.New("backup not retryable")
	// ErrRemoteMissing means the file is gone from the user's cloud storage.
	ErrRemoteMissing = errors.New("remote file missing")
	// ErrRevokeIncomplete means the user's data was removed locally but revoking
	// Drive access failed; callers treat it as success and only log it.
	ErrRevokeIncomplete = errors.New("drive revoke incomplete")
)
