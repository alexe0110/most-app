//go:build integration

package postgres_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
)

// saveDoc inserts a saved document for u with the given backup_status.
func saveDoc(t *testing.T, repo *postgres.Repo, u postgres.User, uniq, backup string) {
	t.Helper()
	ctx := t.Context()
	d, err := repo.InsertDocument(ctx, postgres.InsertDocumentParams{
		UserID: u.ID, Status: "saved", Kind: "document", Category: "passport",
		Title: "t", TgFileID: "f-" + uniq, TgFileUniqueID: uniq,
	})
	require.NoError(t, err)
	require.NoError(t, repo.UpdateDocumentBackupStatus(ctx, postgres.UpdateDocumentBackupStatusParams{
		ID: d.ID, BackupStatus: backup,
	}))
}

func TestRepo_DeleteUserData(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	u := makeUser(t, repo, 400)
	other := makeUser(t, repo, 401)
	saveDoc(t, repo, u, "u-doc", "backed_up")
	saveDoc(t, repo, other, "o-doc", "none")
	_, err := repo.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
		UserID: u.ID, Bot: "docs", Provider: "google", RefreshTokenEnc: []byte{0x01}, Status: "active",
	})
	require.NoError(t, err)
	_, err = repo.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
		UserID: u.ID, Category: "passport", DriveFolderID: "folder-1",
	})
	require.NoError(t, err)

	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(u.ID.String())))

	// The user's own data is gone.
	n, err := repo.CountSaved(ctx, docsbot.UserID(u.ID.String()))
	require.NoError(t, err)
	require.Equal(t, 0, n, "documents deleted")
	_, err = repo.GetIntegration(ctx, postgres.GetIntegrationParams{UserID: u.ID, Bot: "docs", Provider: "google"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "integration deleted")
	_, err = repo.GetDriveFolder(ctx, postgres.GetDriveFolderParams{UserID: u.ID, Category: "passport"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "drive folder deleted")
	_, err = repo.GetUserByTgID(ctx, 400)
	require.ErrorIs(t, err, pgx.ErrNoRows, "user is soft-deleted (hidden)")

	// A second user's data is untouched.
	n, err = repo.CountSaved(ctx, docsbot.UserID(other.ID.String()))
	require.NoError(t, err)
	require.Equal(t, 1, n, "other user's documents survive")
}

// TestRepo_EnsureUserRevivesAfterDelete covers the /start-after-deletion case:
// a soft-deleted user who starts again gets a clean account (same id, no data).
func TestRepo_EnsureUserRevivesAfterDelete(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	first, err := repo.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 500, Lang: "ru"})
	require.NoError(t, err)
	saveDoc(t, repo, first, "doc", "none")

	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(first.ID.String())))

	again, err := repo.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 500, Lang: "ru"})
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID, "same account is revived")
	require.False(t, again.DeletedAt.Valid, "deleted_at is cleared")

	n, err := repo.CountSaved(ctx, docsbot.UserID(again.ID.String()))
	require.NoError(t, err)
	require.Equal(t, 0, n, "revived account has no old documents")
}

func TestRepo_BackupStatusCounts(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 600)
	saveDoc(t, repo, u, "a", "backed_up")
	saveDoc(t, repo, u, "b", "backed_up")
	saveDoc(t, repo, u, "c", "failed")
	saveDoc(t, repo, u, "d", "none")

	counts, err := repo.BackupStatusCounts(ctx, docsbot.UserID(u.ID.String()))
	require.NoError(t, err)
	got := map[docsbot.BackupStatus]int{}
	for _, c := range counts {
		got[c.Status] = c.Count
	}
	require.Equal(t, 2, got[docsbot.BackupBackedUp])
	require.Equal(t, 1, got[docsbot.BackupFailed])
	require.Equal(t, 1, got[docsbot.BackupNone])
}
