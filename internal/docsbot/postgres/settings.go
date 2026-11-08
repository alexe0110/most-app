package postgres

import (
	"context"
	"fmt"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// This file implements the docsbot.Repo methods behind /settings: the backup
// breakdown, the failed-upload and export listings, and the one-transaction
// deletion of all of a user's data.

// BackupStatusCounts returns the user's saved-document counts grouped by backup_status.
func (r *Repo) BackupStatusCounts(ctx context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.DocumentBackupStatusCounts(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("backup status counts: %w", err)
	}
	out := make([]docsbot.BackupStatusCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, docsbot.BackupStatusCount{
			Status: docsbot.BackupStatus(row.BackupStatus),
			Count:  int(row.N),
		})
	}
	return out, nil
}

// FailedIDs lists the user's saved documents whose Drive upload failed, oldest first.
func (r *Repo) FailedIDs(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ListFailedDocumentIDs(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list failed documents: %w", err)
	}
	out := make([]docsbot.DocumentID, 0, len(rows))
	for _, id := range rows {
		out = append(out, docsbot.DocumentID(id.String()))
	}
	return out, nil
}

// SavedForExport lists up to limit of the user's saved documents, oldest first.
func (r *Repo) SavedForExport(ctx context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ListSavedDocumentsForExport(ctx, ListSavedDocumentsForExportParams{UserID: uid, Lim: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list saved for export: %w", err)
	}
	return toDocuments(rows), nil
}

// DeleteUserData deletes the user's documents, drive_folders and integrations and
// soft-deletes the user, all in one transaction.
func (r *Repo) DeleteUserData(ctx context.Context, user docsbot.UserID) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	return r.Tx(ctx, func(q *Queries) error {
		if err := q.DeleteUserDocuments(ctx, uid); err != nil {
			return fmt.Errorf("delete user documents: %w", err)
		}
		if err := q.DeleteUserDriveFolders(ctx, uid); err != nil {
			return fmt.Errorf("delete user drive folders: %w", err)
		}
		if err := q.DeleteUserIntegrations(ctx, uid); err != nil {
			return fmt.Errorf("delete user integrations: %w", err)
		}
		if err := q.SoftDeleteUser(ctx, uid); err != nil {
			return fmt.Errorf("soft delete user: %w", err)
		}
		return nil
	})
}
