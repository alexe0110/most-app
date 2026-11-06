package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo persists docsbot entities in Postgres. It embeds the sqlc-generated
// *Queries, so every query is available on Repo, and adds Tx for atomicity.
type Repo struct {
	pool *pgxpool.Pool
	*Queries
}

// NewRepo wires a Repo around an existing pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool, Queries: New(pool)}
}

// Tx runs fn in one transaction: fn gets a *Queries bound to it, an error rolls
// back, nil commits.
func (r *Repo) Tx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	if err := fn(r.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}
