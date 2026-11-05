//go:build integration

package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

func TestRepo_Ensure(t *testing.T) {
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	ctx := t.Context()

	// First contact creates the user.
	id1, err := repo.Ensure(ctx, 500, "ru")
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// A repeat /start upserts in place: same id, no duplicate row.
	id2, err := repo.Ensure(ctx, 500, "en")
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "select count(*) from users where tg_user_id = $1", int64(500)).Scan(&count))
	require.Equal(t, 1, count)

	// A returning (soft-deleted) user is revived by Ensure, keeping the same id.
	u, err := repo.GetUserByTgID(ctx, 500)
	require.NoError(t, err)
	require.NoError(t, repo.SoftDeleteUser(ctx, u.ID))
	_, err = repo.GetUserByTgID(ctx, 500)
	require.ErrorIs(t, err, pgx.ErrNoRows, "soft-deleted user is hidden")

	id3, err := repo.Ensure(ctx, 500, "ru")
	require.NoError(t, err)
	require.Equal(t, id1, id3)
	_, err = repo.GetUserByTgID(ctx, 500)
	require.NoError(t, err, "Ensure revived the user")
}

func TestRepo_Ensure_defaultLang(t *testing.T) {
	repo := postgres.NewRepo(dbtest.New(t))
	ctx := t.Context()

	_, err := repo.Ensure(ctx, 600, "")
	require.NoError(t, err)

	u, err := repo.GetUserByTgID(ctx, 600)
	require.NoError(t, err)
	require.Equal(t, "ru", u.Lang, "empty Telegram lang falls back to ru")
}
