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
)
