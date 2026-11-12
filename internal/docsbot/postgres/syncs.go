package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// uniqueViolation is the SQLSTATE of a unique constraint violation.
const uniqueViolation = "23505"

// The insert callbacks below enqueue a job inside the open transaction. River
// is not imported here; the jobs adapter supplies them.

// inTx runs fn in one transaction, committing when it returns nil.
func (r *Repo) inTx(ctx context.Context, fn func(q *Queries, tx pgx.Tx) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := fn(r.WithTx(tx), tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// needsSync reports whether the update left a backed-up document sync-pending.
func needsSync(backup, sync string) bool {
	return backup == string(docsbot.BackupBackedUp) && sync == string(docsbot.SyncPending)
}

// SetTitleForSync renames the document and, if its Drive file must follow, runs
// insert in the same transaction. queued reports whether a sync is pending.
func (r *Repo) SetTitleForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string, insert func(ctx context.Context, tx pgx.Tx) error) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		row, err := q.UpdateTitleForSync(ctx, UpdateTitleForSyncParams{ID: docID, UserID: uid, Title: title})
		if err != nil {
			return notFoundOr(err, "update document title")
		}
		queued = needsSync(row.BackupStatus, row.SyncStatus)
		if !queued {
			return nil
		}
		return insert(ctx, tx)
	})
	return queued, err
}

// SetCategoryForSync recategorizes the document, queuing a sync like SetTitleForSync.
func (r *Repo) SetCategoryForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category, insert func(ctx context.Context, tx pgx.Tx) error) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		row, err := q.UpdateCategoryForSync(ctx, UpdateCategoryForSyncParams{ID: docID, UserID: uid, Category: string(cat)})
		if err != nil {
			return notFoundOr(err, "update document category")
		}
		queued = needsSync(row.BackupStatus, row.SyncStatus)
		if !queued {
			return nil
		}
		return insert(ctx, tx)
	})
	return queued, err
}

// MarkBackedUpForSync records a finished upload and, when meta_version moved
// since startVersion, leaves the document sync-pending and runs insert in the
// same transaction. pages > 0 records the PDF's page count. ErrNotFound if the
// document is gone.
func (r *Repo) MarkBackedUpForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int, insert func(ctx context.Context, tx pgx.Tx) error) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		version, err := q.MarkDocumentBackedUpSync(ctx, MarkDocumentBackedUpSyncParams{
			ID:           docID,
			UserID:       uid,
			DriveFileID:  pgtype.Text{String: driveFileID, Valid: true},
			StartVersion: int32(startVersion),
			PageCount:    pgtype.Int4{Int32: int32(pages), Valid: pages > 0},
		})
		if err != nil {
			return notFoundOr(err, "mark document backed up")
		}
		queued = int(version) != startVersion
		if !queued {
			return nil
		}
		return insert(ctx, tx)
	})
	return queued, err
}

// ReplaceContentForSync swaps the document's file and, for a backed-up document,
// runs insert in the same transaction when cloud is set (Drive connected). replaced is false when the update touched
// no row (gone, uploading, sync in flight or file missing).
func (r *Repo) ReplaceContentForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool, insert func(ctx context.Context, tx pgx.Tx) error) (replaced bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		backup, err := q.ReplaceDocumentContent(ctx, ReplaceDocumentContentParams{
			Cloud:          cloud,
			Kind:           string(in.Kind),
			ID:             docID,
			UserID:         uid,
			TgFileID:       in.FileID,
			TgFileUniqueID: in.FileUniqueID,
			FileSize:       pgtype.Int8{Int64: in.FileSize, Valid: in.FileSize > 0},
			MimeType:       pgtype.Text{String: in.MIME, Valid: in.MIME != ""},
			PageCount:      int32(in.PageCount),
			ImageWidth:     int32(in.Width),
			ImageHeight:    int32(in.Height),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return docsbot.ErrDuplicateFile
		}
		if err != nil {
			return fmt.Errorf("replace document content: %w", err)
		}
		replaced = true
		if !cloud || backup != string(docsbot.BackupBackedUp) {
			return nil
		}
		return insert(ctx, tx)
	})
	return replaced && err == nil, err
}

// EnqueueSyncJob marks a backed-up document sync-pending and runs insert in the
// same transaction. A document that is gone, not backed up or missing on Drive
// queues nothing.
func (r *Repo) EnqueueSyncJob(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) error) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	return r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		n, err := q.MarkSyncPending(ctx, MarkSyncPendingParams{ID: docID, UserID: uid})
		if err != nil {
			return fmt.Errorf("mark sync pending: %w", err)
		}
		if n == 0 {
			return nil
		}
		return insert(ctx, tx)
	})
}

// RetrySyncJob moves a failed sync back to pending and runs insert in the same
// transaction. It reports false when the sync was not failed.
func (r *Repo) RetrySyncJob(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) error) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		n, err := q.ClaimSyncFailed(ctx, ClaimSyncFailedParams{ID: docID, UserID: uid})
		if err != nil {
			return fmt.Errorf("claim failed sync: %w", err)
		}
		if n == 0 {
			return nil
		}
		queued = true
		return insert(ctx, tx)
	})
	return queued, err
}

// DeleteForSync deletes the document row and reports, under the row lock, what
// its Drive copy was. When queueTrash is set and the document had a live Drive
// file, insert runs in the same transaction with that file's id; otherwise
// remoteKept tells that a live file stays in Drive. ErrNotFound if the user has
// no such document.
func (r *Repo) DeleteForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, queueTrash bool, insert func(ctx context.Context, tx pgx.Tx, remoteID string) error) (remoteKept bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		row, err := q.DeleteDocumentReturning(ctx, DeleteDocumentReturningParams{ID: docID, UserID: uid})
		if err != nil {
			return notFoundOr(err, "delete document")
		}
		live := row.BackupStatus == string(docsbot.BackupBackedUp) && row.DriveFileID.Valid &&
			row.DriveFileID.String != "" && row.SyncStatus != string(docsbot.SyncMissing)
		if !live {
			return nil
		}
		if !queueTrash {
			remoteKept = true
			return nil
		}
		return insert(ctx, tx, row.DriveFileID.String)
	})
	return remoteKept && err == nil, err
}
