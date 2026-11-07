package postgres

import (
	"context"
	"errors"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// errTODO15 marks contract stubs filled by the postgres step of #15.
var errTODO15 = errors.New("postgres: #15 not implemented")

// BackupStatusCounts returns saved-document counts grouped by backup_status.
func (r *Repo) BackupStatusCounts(ctx context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
	return nil, errTODO15
}

// FailedIDs lists the user's failed-upload documents, oldest first.
func (r *Repo) FailedIDs(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return nil, errTODO15
}

// SavedForExport lists up to limit of the user's saved documents, oldest first.
func (r *Repo) SavedForExport(ctx context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	return nil, errTODO15
}

// DeleteUserData deletes the user's documents, drive_folders and integrations and
// soft-deletes the user, in one transaction.
func (r *Repo) DeleteUserData(ctx context.Context, user docsbot.UserID) error {
	return errTODO15
}
