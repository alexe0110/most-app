package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
	err = r.inTx(ctx, func(q *Queries, tx pgx.Tx) error {
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
	return r.inTx(ctx, func(_ *Queries, tx pgx.Tx) error {
		_, err := r.jobs.InsertUpload(ctx, tx, user, id)
		return err
	})
}
