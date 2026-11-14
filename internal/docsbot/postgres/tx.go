package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// inTx runs fn in one transaction: fn gets Queries bound to it and the tx itself
// (for job inserts); an error rolls back, nil commits. It is the only place that
// opens a transaction, so every multi-write method shares one commit and rollback
// path.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *Queries, tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	if err := fn(New(tx), tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
