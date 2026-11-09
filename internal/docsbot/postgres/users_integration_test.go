//go:build integration

package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
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

// purgeSQL mirrors the purge_users job in platform/jobs.
const purgeSQL = `delete from users where deleted_at is not null`

// TestUser_deletedThenMessageAroundPurge covers D4: a message from a deleted
// user before the purge revives the same empty account, which the purge then
// leaves alone; after the purge the same message creates a fresh user row.
func TestUser_deletedThenMessageAroundPurge(t *testing.T) {
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	ctx := t.Context()

	first, err := repo.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 800, Lang: "ru"})
	require.NoError(t, err)
	saveDoc(t, repo, first, "old", "none")
	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(first.ID.String())))

	// Before the purge: revived, same id, no old documents.
	revived, err := repo.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 800, Lang: "ru"})
	require.NoError(t, err)
	require.Equal(t, first.ID, revived.ID)
	n, err := repo.CountSaved(ctx, docsbot.UserID(revived.ID.String()))
	require.NoError(t, err)
	require.Zero(t, n)

	// The purge does not touch the revived user.
	_, err = pool.Exec(ctx, purgeSQL)
	require.NoError(t, err)
	_, err = repo.GetUserByTgID(ctx, 800)
	require.NoError(t, err, "revived user survives the purge")

	// Delete again, purge, then message: a brand-new row with no data.
	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(first.ID.String())))
	_, err = pool.Exec(ctx, purgeSQL)
	require.NoError(t, err)

	fresh, err := repo.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 800, Lang: "ru"})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, fresh.ID, "purged user comes back as a new row")
	n, err = repo.CountSaved(ctx, docsbot.UserID(fresh.ID.String()))
	require.NoError(t, err)
	require.Zero(t, n)
}
