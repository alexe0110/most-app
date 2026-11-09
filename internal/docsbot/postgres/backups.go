package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// EnqueueUpload marks the document backup_status=pending and runs insert inside
// the same transaction, so the status change and the enqueued job commit or roll
// back together. River is not imported here; the caller supplies the insert.
// If insert reports skip (the job is a duplicate of one already queued), the
// transaction is rolled back and the document status stays untouched.
func (r *Repo) EnqueueUpload(ctx context.Context, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) (skip bool, err error)) error {
	docID, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	err = r.WithTx(tx).UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{
		ID:           docID,
		BackupStatus: string(docsbot.BackupPending),
	})
	if err != nil {
		return fmt.Errorf("mark backup pending: %w", err)
	}
	skip, err := insert(ctx, tx)
	if err != nil {
		return err
	}
	if skip {
		return nil // deferred Rollback also reverts the pending update
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
