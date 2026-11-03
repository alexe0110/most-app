//go:build integration

package postgres_test

import "testing"

// TestRepo_Save is a skeleton showing where an outbound adapter's integration
// tests live. Flesh it out in #5 once the Repo exists, e.g.:
//
//	pool := dbtest.New(t)
//	repo := postgres.NewRepo(pool)
//	err := repo.Save(t.Context(), doc)
//	require.NoError(t, err)
func TestRepo_Save(t *testing.T) {
	t.Skip("TODO #5: postgres Repo not implemented yet")
}
