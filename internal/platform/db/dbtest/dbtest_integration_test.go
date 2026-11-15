//go:build integration

package dbtest_test

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// TestNew proves the integration harness works end to end: a real Postgres
// boots in a container and answers a query through the pool.
func TestNew(t *testing.T) {
	pool := dbtest.New(t)

	var n int
	err := pool.QueryRow(t.Context(), "select 1").Scan(&n)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

// TestNew_isolatedMigratedDatabases proves every call gets its own database
// with the full schema, so tests cannot see each other's rows.
func TestNew_isolatedMigratedDatabases(t *testing.T) {
	first := dbtest.New(t)
	second := dbtest.New(t)

	for _, table := range []string{"users", "river_job"} {
		var exists bool
		err := first.QueryRow(t.Context(), "select to_regclass($1) is not null", table).Scan(&exists)
		require.NoError(t, err)
		require.True(t, exists, "table %s must come from the template", table)
	}

	_, err := first.Exec(t.Context(), "insert into users (tg_user_id) values (1)")
	require.NoError(t, err)

	var inFirst, inSecond int
	require.NoError(t, first.QueryRow(t.Context(), "select count(*) from users").Scan(&inFirst))
	require.NoError(t, second.QueryRow(t.Context(), "select count(*) from users").Scan(&inSecond))
	require.Equal(t, 1, inFirst)
	require.Zero(t, inSecond)
}

// Tests may clone the template in parallel, and a finished test leaves no
// database behind: the container would otherwise fill up over a long run.
func TestNew_parallelClonesAreDropped(t *testing.T) {
	own := dbtest.New(t)

	t.Run("clones", func(t *testing.T) {
		for i := range 8 {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()
				pool := dbtest.New(t)
				_, err := pool.Exec(t.Context(), "insert into users (tg_user_id) values ($1)", i)
				require.NoError(t, err)
			})
		}
	})

	var left int
	require.NoError(t, own.QueryRow(t.Context(), `select count(*) from pg_database where datname like 't\_%'`).Scan(&left))
	require.Equal(t, 1, left, "only this test's own database is left")
}
