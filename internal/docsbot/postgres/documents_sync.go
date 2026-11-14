package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// This file holds the Documents methods that change a row and queue a job in one
// transaction (the docsbot Syncs and Uploads ports).

// needsSync reports whether the update left a backed-up document sync-pending.
func needsSync(backup, sync string) bool {
	return backup == string(docsbot.BackupBackedUp) && sync == string(docsbot.SyncPending)
}

// SetTitle renames the document. With cloud set, a backed-up document whose
// Drive file is live also goes sync-pending and a sync job is queued in the same
// transaction; queued reports that. ErrNotFound if the user has no such document.
func (r *Documents) SetTitle(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string, cloud bool) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) SetCategory(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category, cloud bool) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) MarkBackedUp(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) ReplaceContent(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool) (replaced bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
		if isUniqueViolation(err) {
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
func (r *Documents) EnqueueSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	return inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) RetrySync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (queued bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) DeleteDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cloud bool) (remoteKept bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
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
func (r *Documents) RequeueSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return inTx(ctx, r.pool, func(_ *Queries, tx pgx.Tx) error {
		return r.jobs.InsertSync(ctx, tx, user, id)
	})
}

// SaveChosen saves a pending document under the chosen category and writes the
// title and backup outcome that go with it in the same transaction, so a failed
// job insert leaves the document pending for the next tap. saved is false when
// the document was not pending.
func (r *Documents) SaveChosen(ctx context.Context, c docsbot.SaveChoice) (saved bool, err error) {
	docID, uid, err := ids(c.ID, c.User)
	if err != nil {
		return false, err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
		n, err := q.SaveDocumentCategory(ctx, SaveDocumentCategoryParams{ID: docID, UserID: uid, Category: string(c.Category)})
		if err != nil {
			return fmt.Errorf("save document category: %w", err)
		}
		if n == 0 {
			return nil
		}
		saved = true
		if c.Title != "" {
			// Cloud stays false: a document saved a moment ago cannot be backed up yet,
			// so there is no Drive file for a sync to rename.
			if _, err := q.UpdateDocumentTitle(ctx, UpdateDocumentTitleParams{ID: docID, UserID: uid, Title: c.Title, Cloud: false}); err != nil {
				return notFoundOr(err, "set default title")
			}
		}
		return r.applyBackup(ctx, q, tx, c, docID, uid)
	})
	return saved && err == nil, err
}

// applyBackup writes the backup outcome of a saved choice: nothing for None, the
// too_large status, or pending together with the upload job.
func (r *Documents) applyBackup(ctx context.Context, q *Queries, tx pgx.Tx, c docsbot.SaveChoice, docID, uid pgtype.UUID) error {
	switch c.Backup {
	case docsbot.BackupNone:
		return nil
	case docsbot.BackupTooLarge, docsbot.BackupPending:
		err := q.UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{ID: docID, UserID: uid, BackupStatus: string(c.Backup)})
		if err != nil {
			return fmt.Errorf("set backup status: %w", err)
		}
		if c.Backup == docsbot.BackupTooLarge {
			return nil
		}
		// A live job for a document that was pending a moment ago cannot exist; if
		// one does, it will upload this document, so the insert is not rolled back.
		_, err = r.jobs.InsertUpload(ctx, tx, c.User, c.ID)
		return err
	default:
		return fmt.Errorf("save chosen: unsupported backup status %q", c.Backup)
	}
}

// RequeueUpload inserts the upload job only, unique by args among live states,
// and does not touch the document, so a live job dedups to a no-op and the
// stuck-since clock (updated_at) is preserved.
func (r *Documents) RequeueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return inTx(ctx, r.pool, func(_ *Queries, tx pgx.Tx) error {
		_, err := r.jobs.InsertUpload(ctx, tx, user, id)
		return err
	})
}
