// Package dbtest spins up an ephemeral Postgres container for integration
// tests and returns a ready-to-use connection pool. Unit tests never call it.
package dbtest

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alexe0110/most-app/internal/platform/db"
	"github.com/alexe0110/most-app/internal/platform/jobs"
)

// New starts a disposable Postgres (postgres:17) and returns a connected pool.
// The pool and container are cleaned up via t.Cleanup.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := t.Context()

	ctr, err := postgres.Run(ctx, "postgres:17",
		postgres.WithDatabase("most"),
		postgres.WithUsername("most"),
		postgres.WithPassword("most"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, testcontainers.TerminateContainer(ctr))
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx))

	// Bring the schema (and River queue tables) up to date.
	require.NoError(t, db.Migrate(ctx, dsn))
	require.NoError(t, jobs.Migrate(ctx, pool))

	return pool
}
