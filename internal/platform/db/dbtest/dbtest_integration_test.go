//go:build integration

package dbtest_test

import (
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
