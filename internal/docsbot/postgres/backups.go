package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// SaveChosen saves a pending document under the chosen category and writes the
// title and backup outcome that go with it in the same transaction, so a failed
// job insert leaves the document pending for the next tap. saved is false when
// the document was not pending.
func (r *Repo) SaveChosen(ctx context.Context, c docsbot.SaveChoice) (saved bool, err error) {
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
			if _, err := q.UpdateDocumentTitle(ctx, UpdateDocumentTitleParams{ID: docID, UserID: uid, Title: c.Title}); err != nil {
				return notFoundOr(err, "set default title")
			}
		}
		return r.applyBackup(ctx, q, tx, c, docID, uid)
	})
	return saved && err == nil, err
}

// applyBackup writes the backup outcome of a saved choice: nothing for None, the
// too_large status, or pending together with the upload job.
func (r *Repo) applyBackup(ctx context.Context, q *Queries, tx pgx.Tx, c docsbot.SaveChoice, docID, uid pgtype.UUID) error {
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

// errUploadSkipped aborts the EnqueueUpload transaction when a live upload job
// already exists; it never leaves this file.
var errUploadSkipped = errors.New("upload job already queued")

// EnqueueUpload marks the document backup_status=pending and queues its upload in
// the same transaction, so the status change and the job commit or roll back
// together. A live upload job is unique by args: the repeated call is skipped and
// the transaction rolled back, so the document status stays untouched.
func (r *Repo) EnqueueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	err = inTx(ctx, r.pool, func(q *Queries, tx pgx.Tx) error {
		err := q.UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{
			ID:           docID,
			UserID:       uid,
			BackupStatus: string(docsbot.BackupPending),
		})
		if err != nil {
			return fmt.Errorf("mark backup pending: %w", err)
		}
		skipped, err := r.jobs.InsertUpload(ctx, tx, user, id)
		if err != nil {
			return err
		}
		if skipped {
			return errUploadSkipped
		}
		return nil
	})
	if errors.Is(err, errUploadSkipped) {
		return nil
	}
	return err
}

// RequeueUpload inserts the upload job only, with the same uniqueness as
// EnqueueUpload and without touching the document, so a live job dedups to a
// no-op and the stuck-since clock (updated_at) is preserved.
func (r *Repo) RequeueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return inTx(ctx, r.pool, func(_ *Queries, tx pgx.Tx) error {
		_, err := r.jobs.InsertUpload(ctx, tx, user, id)
		return err
	})
}
