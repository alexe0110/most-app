package postgres

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// This file holds the backup and sync status operations of Documents that queue no job.

// SetBackupStatus updates the Drive upload status.
func (r *Documents) SetBackupStatus(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.q.UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{ID: docID, UserID: uid, BackupStatus: string(s)}); err != nil {
		return fmt.Errorf("update backup status: %w", err)
	}
	return nil
}

// FailBackup marks the upload failed and stores why (a docsbot.BackupReason*).
func (r *Documents) FailBackup(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, reason docsbot.BackupReason) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.q.FailDocumentBackup(ctx, FailDocumentBackupParams{ID: docID, UserID: uid, BackupReason: pgtype.Text{String: string(reason), Valid: reason != ""}}); err != nil {
		return fmt.Errorf("fail document backup: %w", err)
	}
	return nil
}

// ClaimFailed atomically moves a failed document to pending; false when it is
// not failed or not the user's.
func (r *Documents) ClaimFailed(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	n, err := r.q.ClaimFailedForRetry(ctx, ClaimFailedForRetryParams{ID: docID, UserID: uid})
	if err != nil {
		return false, fmt.Errorf("claim failed for retry: %w", err)
	}
	return n > 0, nil
}

// MarkUploading sets the status to uploading without touching updated_at, so
// the sweeper's 24h backstop counts from enqueue time.
func (r *Documents) MarkUploading(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.q.MarkDocumentUploading(ctx, MarkDocumentUploadingParams{ID: docID, UserID: uid}); err != nil {
		return fmt.Errorf("mark document uploading: %w", err)
	}
	return nil
}

// StuckUploads lists pending/uploading saved documents last touched before
// olderThan, for users with an active Drive integration.
func (r *Documents) StuckUploads(ctx context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	rows, err := r.q.StuckUploads(ctx, StuckUploadsParams{
		OlderThan: pgtype.Timestamptz{Time: olderThan, Valid: true},
		Lim:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("stuck uploads: %w", err)
	}
	out := make([]docsbot.StuckUpload, 0, len(rows))
	for _, row := range rows {
		out = append(out, docsbot.StuckUpload{
			User:      docsbot.UserID(row.UserID.String()),
			ID:        docsbot.DocumentID(row.ID.String()),
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}

// StuckSyncs lists backed-up documents whose sync has been pending since before
// olderThan, for users with an active Drive integration.
func (r *Documents) StuckSyncs(ctx context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	rows, err := r.q.StuckSyncs(ctx, StuckSyncsParams{
		OlderThan: pgtype.Timestamptz{Time: olderThan, Valid: true},
		Lim:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("stuck syncs: %w", err)
	}
	out := make([]docsbot.StuckUpload, 0, len(rows))
	for _, row := range rows {
		out = append(out, docsbot.StuckUpload{
			User:      docsbot.UserID(row.UserID.String()),
			ID:        docsbot.DocumentID(row.ID.String()),
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}

// ResetPendingUploads resets the user's pending/uploading saved documents to none.
func (r *Documents) ResetPendingUploads(ctx context.Context, user docsbot.UserID) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	if err := r.q.ResetPendingUploads(ctx, uid); err != nil {
		return fmt.Errorf("reset pending uploads: %w", err)
	}
	return nil
}

// MarkSynced records a finished sync if the document is unchanged since version.
func (r *Documents) MarkSynced(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, version int) (bool, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	n, err := r.q.MarkSynced(ctx, MarkSyncedParams{ID: docID, UserID: uid, MetaVersion: int32(version)})
	if err != nil {
		return false, fmt.Errorf("mark synced: %w", err)
	}
	return n > 0, nil
}

// SetSyncStatus sets sync_status and sync_reason ("" clears the reason).
func (r *Documents) SetSyncStatus(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.SyncStatus, reason string) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	err = r.q.SetSyncStatus(ctx, SetSyncStatusParams{
		ID: docID, UserID: uid, SyncStatus: string(s), SyncReason: pgtype.Text{String: reason, Valid: reason != ""},
	})
	if err != nil {
		return fmt.Errorf("set sync status: %w", err)
	}
	return nil
}

// TransitionSyncStatus moves sync_status from -> to for a backed-up document and
// reports whether the row still had the from status and the checked Drive file.
func (r *Documents) TransitionSyncStatus(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, remoteID string, from, to docsbot.SyncStatus) (bool, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	n, err := r.q.TransitionSyncStatus(ctx, TransitionSyncStatusParams{
		ID: docID, UserID: uid, FromStatus: string(from), ToStatus: string(to), DriveFileID: pgtype.Text{String: remoteID, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("transition sync status: %w", err)
	}
	return n > 0, nil
}

// ClaimTransferable atomically moves every saved none/failed document to
// pending and returns their ids, oldest first.
func (r *Documents) ClaimTransferable(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ClaimTransferableDocuments(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("claim transferable documents: %w", err)
	}
	ids := make([]claimedID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, claimedID{id: row.ID, createdAt: row.CreatedAt.Time})
	}
	return sortedIDs(ids), nil
}

// ClaimAllFailed atomically moves every failed upload to pending and returns
// their ids, oldest first.
func (r *Documents) ClaimAllFailed(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ClaimFailedDocuments(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("claim failed documents: %w", err)
	}
	ids := make([]claimedID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, claimedID{id: row.ID, createdAt: row.CreatedAt.Time})
	}
	return sortedIDs(ids), nil
}

// claimedID is a claimed document with the time to order the batch by.
type claimedID struct {
	id        pgtype.UUID
	createdAt time.Time
}

// sortedIDs orders claimed documents oldest first (UPDATE ... RETURNING has no order).
func sortedIDs(in []claimedID) []docsbot.DocumentID {
	slices.SortFunc(in, func(a, b claimedID) int { return a.createdAt.Compare(b.createdAt) })
	out := make([]docsbot.DocumentID, 0, len(in))
	for _, c := range in {
		out = append(out, docsbot.DocumentID(c.id.String()))
	}
	return out
}
