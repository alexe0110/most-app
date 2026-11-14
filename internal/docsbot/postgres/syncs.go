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

var (
	_ docsbot.Syncs   = (*Repo)(nil)
	_ docsbot.Uploads = (*Repo)(nil)
)

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

// SetTitle renames the document. With cloud set, a backed-up document whose
// Drive file is live also goes sync-pending and a sync job is queued in the same
// transaction; queued reports that. ErrNotFound if the user has no such document.
func (r *Repo) SetTitle(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string, cloud bool) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		row, err := q.UpdateDocumentTitle(ctx, UpdateDocumentTitleParams{ID: docID, UserID: uid, Title: title, Cloud: cloud})
		if err != nil {
			return notFoundOr(err, "update document title")
		}
		queued = cloud && needsSync(row.BackupStatus, row.SyncStatus)
		if !queued {
			return nil
		}
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
	return queued && err == nil, err
}

// SetCategory recategorizes the document, queuing a sync like SetTitle.
func (r *Repo) SetCategory(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category, cloud bool) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
		row, err := q.UpdateDocumentCategory(ctx, UpdateDocumentCategoryParams{ID: docID, UserID: uid, Category: string(cat), Cloud: cloud})
		if err != nil {
			return notFoundOr(err, "update document category")
		}
		queued = cloud && needsSync(row.BackupStatus, row.SyncStatus)
		if !queued {
			return nil
		}
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
	return queued && err == nil, err
}

// MarkBackedUp records a finished upload and, when meta_version moved since
// startVersion, leaves the document sync-pending and queues a sync in the same
// transaction. pages > 0 records the PDF's page count. ErrNotFound if the
// document is gone.
func (r *Repo) MarkBackedUp(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int) (queued bool, err error) {
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
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
	return queued && err == nil, err
}

// ReplaceContent swaps the document's file and, for a backed-up document when
// cloud is set (Drive connected), queues a sync in the same transaction. replaced
// is false when the update touched no row (gone, uploading, sync in flight or
// file missing).
func (r *Repo) ReplaceContent(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool) (replaced bool, err error) {
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
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
	return replaced && err == nil, err
}

// EnqueueSync marks a backed-up document sync-pending and queues a sync in the
// same transaction. A document that is gone, not backed up or missing on Drive
// queues nothing.
func (r *Repo) EnqueueSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
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
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
}

// RetrySync moves a failed sync back to pending and queues it in the same
// transaction. It reports false when the sync was not failed.
func (r *Repo) RetrySync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (queued bool, err error) {
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
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
	return queued && err == nil, err
}

// DeleteDocument deletes the document row and reports, under the row lock, what
// its Drive copy was. With cloud set and a live Drive file, trashing it is queued
// in the same transaction; without cloud, remoteKept tells that a live file stays
// in Drive. ErrNotFound if the user has no such document.
func (r *Repo) DeleteDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cloud bool) (remoteKept bool, err error) {
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
		if !cloud {
			remoteKept = true
			return nil
		}
		return r.jobs.InsertTrash(ctx, tx, user, row.DriveFileID.String)
	})
	return remoteKept && err == nil, err
}

// RequeueSync inserts the sync job only, without touching the document, so the
// stuck clock keeps running.
func (r *Repo) RequeueSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return r.inTx(ctx, func(_ *Queries, tx pgx.Tx) error {
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
}
