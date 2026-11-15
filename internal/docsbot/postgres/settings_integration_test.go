//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// saveDoc inserts a saved document for u with the given backup_status.
func saveDoc(t *testing.T, repo *adapters, u postgres.User, uniq, backup string) {
	t.Helper()
	ctx := t.Context()
	d, err := repo.q.InsertDocument(ctx, postgres.InsertDocumentParams{
		UserID: u.ID, Status: "saved", Kind: "document", Category: "passport",
		Title: "t", TgFileID: "f-" + uniq, TgFileUniqueID: uniq,
	})
	require.NoError(t, err)
	require.NoError(t, repo.q.UpdateDocumentBackupStatus(ctx, postgres.UpdateDocumentBackupStatusParams{
		ID: d.ID, UserID: u.ID, BackupStatus: backup,
	}))
}

func TestRepo_DeleteUserData(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	ctx := t.Context()

	u := makeUser(t, repo, 400)
	other := makeUser(t, repo, 401)
	saveDoc(t, repo, u, "u-doc", "backed_up")
	saveDoc(t, repo, other, "o-doc", "none")
	for _, owner := range []postgres.User{u, other} {
		_, err := repo.q.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
			UserID: owner.ID, Bot: "docs", Provider: "google", RefreshTokenEnc: []byte{0x01}, Status: "active",
		})
		require.NoError(t, err)
		_, err = repo.q.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
			UserID: owner.ID, Category: "passport", DriveFolderID: "folder-1",
		})
		require.NoError(t, err)
	}
	var err error
	require.NoError(t, repo.q.SetUserSource(ctx, postgres.SetUserSourceParams{ID: u.ID, Source: pgtype.Text{String: "ad_1", Valid: true}}))
	for _, owner := range []*postgres.User{&u, &other} {
		uid := docsbot.UserID(owner.ID.String())
		_, err = repo.Create(ctx, uid, owner.TgUserID, 11, docsbot.StatusSingle)
		require.NoError(t, err)
		require.NoError(t, repo.SetInlineEnabled(ctx, uid, false))
		_, err = pool.Exec(ctx, "update users set first_start_at = now() where id = $1", owner.ID)
		require.NoError(t, err)
	}
	for _, owner := range []pgtype.UUID{u.ID, other.ID, {}} { // the last is an anonymous event
		require.NoError(t, repo.q.InsertEvent(ctx, postgres.InsertEventParams{UserID: owner, Bot: "docs", Name: "search", Props: []byte("{}")}))
	}

	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(u.ID.String())))

	// The user's own data is gone.
	var events int
	require.NoError(t, pool.QueryRow(ctx, "select count(*) from events where user_id = $1", u.ID).Scan(&events))
	require.Zero(t, events, "events deleted")
	require.NoError(t, pool.QueryRow(ctx, "select count(*) from events").Scan(&events))
	require.Equal(t, 2, events, "other user's and anonymous events survive")
	var source *string
	var firstStart *time.Time
	require.NoError(t, pool.QueryRow(ctx, "select source, first_start_at from users where id = $1", u.ID).Scan(&source, &firstStart))
	require.Nil(t, source, "acquisition source cleared")
	require.Nil(t, firstStart, "first-start mark cleared")
	for _, table := range []string{"status_messages", "docs_settings"} {
		var n int
		require.NoError(t, pool.QueryRow(ctx, "select count(*) from "+table+" where user_id = $1", u.ID).Scan(&n))
		require.Zero(t, n, table+" deleted")
		require.NoError(t, pool.QueryRow(ctx, "select count(*) from "+table+" where user_id = $1", other.ID).Scan(&n))
		require.Equal(t, 1, n, "other user's "+table+" survive")
	}
	n, err := repo.CountSaved(ctx, docsbot.UserID(u.ID.String()))
	require.NoError(t, err)
	require.Equal(t, 0, n, "documents deleted")
	_, err = repo.q.GetIntegration(ctx, postgres.GetIntegrationParams{UserID: u.ID, Bot: "docs", Provider: "google"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "integration deleted")
	_, err = repo.q.GetDriveFolder(ctx, postgres.GetDriveFolderParams{UserID: u.ID, Category: "passport"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "drive folder deleted")
	_, err = repo.q.GetUserByTgID(ctx, 400)
	require.ErrorIs(t, err, pgx.ErrNoRows, "user is soft-deleted (hidden)")

	// A second user's data is untouched.
	n, err = repo.CountSaved(ctx, docsbot.UserID(other.ID.String()))
	require.NoError(t, err)
	require.Equal(t, 1, n, "other user's documents survive")
	_, err = repo.q.GetIntegration(ctx, postgres.GetIntegrationParams{UserID: other.ID, Bot: "docs", Provider: "google"})
	require.NoError(t, err, "other user's integration survives")
	_, err = repo.q.GetDriveFolder(ctx, postgres.GetDriveFolderParams{UserID: other.ID, Category: "passport"})
	require.NoError(t, err, "other user's drive folder survives")
	_, err = repo.q.GetUserByTgID(ctx, 401)
	require.NoError(t, err, "other user is not deleted")
}

// TestRepo_EnsureUserRevivesAfterDelete covers the /start-after-deletion case:
// a soft-deleted user who starts again gets a clean account (same id, no data).
func TestRepo_EnsureUserRevivesAfterDelete(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	first, err := repo.q.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 500, Lang: "ru"})
	require.NoError(t, err)
	saveDoc(t, repo, first, "doc", "none")

	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(first.ID.String())))

	again, err := repo.q.EnsureUser(ctx, postgres.EnsureUserParams{TgUserID: 500, Lang: "ru"})
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

func TestRepo_SavedForExport(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	ctx := t.Context()
	u := makeUser(t, repo, 610)
	user := docsbot.UserID(u.ID.String())
	otherUser := docsbot.UserID(makeUser(t, repo, 611).ID.String())

	// Created newest first, so the expected order differs from insertion order.
	newest := titled(t, repo, user, "new", "d")
	oldest := titled(t, repo, user, "old", "d")
	middle := titled(t, repo, user, "mid", "d")
	for id, age := range map[docsbot.DocumentID]string{oldest.ID: "3 days", middle.ID: "2 days", newest.ID: "1 day"} {
		_, err := pool.Exec(ctx, "update documents set created_at = now() - $2::interval where id = $1::uuid", string(id), age)
		require.NoError(t, err)
	}
	titled(t, repo, otherUser, "foreign", "d")
	_, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: user, Kind: docsbot.KindDocument, Title: "t", FileID: "f-p", FileUniqueID: "pending"})
	require.NoError(t, err)
	removed := titled(t, repo, user, "removed", "d")
	_, err = repo.DeleteDocument(ctx, user, removed.ID, false)
	require.NoError(t, err)

	ids := func(limit int) []docsbot.DocumentID {
		t.Helper()
		docs, err := repo.SavedForExport(ctx, user, limit)
		require.NoError(t, err)
		out := make([]docsbot.DocumentID, 0, len(docs))
		for _, d := range docs {
			out = append(out, d.ID)
		}
		return out
	}

	require.Equal(t, []docsbot.DocumentID{oldest.ID, middle.ID, newest.ID}, ids(10),
		"oldest first, only the user's own saved documents")
	require.Equal(t, []docsbot.DocumentID{oldest.ID, middle.ID}, ids(2), "the limit keeps the oldest")
}
