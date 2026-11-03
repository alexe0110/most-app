package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repo is the outbound adapter that persists docsbot entities in Postgres.
// It embeds the sqlc-generated *Queries, so every generated query is available
// on the Repo directly, and adds Tx for use cases that need atomicity.
type Repo struct {
	pool *pgxpool.Pool
	*Queries
}

// NewRepo wires a Repo around an existing pool.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool, Queries: New(pool)}
}

// Tx runs fn inside a single transaction. fn gets a *Queries bound to that
// transaction; returning an error rolls everything back, a nil error commits.
// SQL never leaks out — callers only ever see domain-level values and errors.
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
