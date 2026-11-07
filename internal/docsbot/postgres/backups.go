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
func (r *Repo) EnqueueUpload(ctx context.Context, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) error) error {
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
	if err := insert(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
