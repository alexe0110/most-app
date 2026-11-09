//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
)

func newDoc(t *testing.T, repo *postgres.Repo, u postgres.User, uniq string) docsbot.DocumentID {
	t.Helper()
	d, err := repo.InsertDocument(t.Context(), postgres.InsertDocumentParams{
		UserID: u.ID, Status: "saved", Kind: "document", Category: "passport",
		Title: "t-" + uniq, TgFileID: "f-" + uniq, TgFileUniqueID: uniq,
	})
	require.NoError(t, err)
	return docsbot.DocumentID(d.ID.String())
}

func TestStatusMessages_lifecycle(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 800)
	user := docsbot.UserID(u.ID.String())

	_, ok, err := repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.False(t, ok, "no status message yet")

	sm, err := repo.Create(ctx, user, 800, 11, docsbot.StatusSingle)
	require.NoError(t, err)
	require.NotEmpty(t, sm.ID)
	require.Equal(t, 11, sm.MessageID)
	require.Equal(t, docsbot.StatusSingle, sm.Kind)
	require.EqualValues(t, 800, sm.ChatID)
	require.False(t, sm.Closed)
	require.WithinDuration(t, time.Now(), sm.CreatedAt, time.Minute)

	got, ok, err := repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, sm.ID, got.ID)

	byID, err := repo.ByID(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Equal(t, user, byID.UserID)
	require.Equal(t, docsbot.StatusSingle, byID.Kind, "kind round-trips")

	// Attach: the document shows up, oldest first.
	d1 := newDoc(t, repo, u, "s1")
	d2 := newDoc(t, repo, u, "s2")
	require.NoError(t, repo.Attach(ctx, user, d1, sm.ID))
	require.NoError(t, repo.Attach(ctx, user, d2, sm.ID))
	require.NoError(t, repo.Attach(ctx, user, d2, sm.ID), "re-attach is idempotent")
	docs, err := repo.DocumentsIn(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Len(t, docs, 2)
	require.Equal(t, d1, docs[0].ID)
	require.Equal(t, d2, docs[1].ID)
	require.Equal(t, sm.ID, docs[0].StatusMessageID)

	// SetMessageID.
	require.NoError(t, repo.SetMessageID(ctx, user, sm.ID, 22))
	byID, err = repo.ByID(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Equal(t, 22, byID.MessageID)

	// Close: no longer offered for grouping, still loadable by id.
	require.NoError(t, repo.Close(ctx, user, sm.ID))
	_, ok, err = repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.False(t, ok)
	byID, err = repo.ByID(ctx, user, sm.ID)
	require.NoError(t, err)
	require.True(t, byID.Closed)
	docs, err = repo.DocumentsIn(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Len(t, docs, 2, "documents stay linked after close")
}

func TestStatusMessages_openIsNewestAndPerUser(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 810)
	other := makeUser(t, repo, 811)
	user, otherID := docsbot.UserID(u.ID.String()), docsbot.UserID(other.ID.String())

	older, err := repo.Create(ctx, user, 810, 1, docsbot.StatusBatch)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // created_at is now(); keep the order unambiguous
	newer, err := repo.Create(ctx, user, 810, 2, docsbot.StatusBatch)
	require.NoError(t, err)
	foreign, err := repo.Create(ctx, otherID, 811, 3, docsbot.StatusBatch)
	require.NoError(t, err)

	got, ok, err := repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, newer.ID, got.ID, "newest open message wins")

	require.NoError(t, repo.Close(ctx, user, newer.ID))
	got, ok, err = repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, older.ID, got.ID, "closed message is skipped")

	got, ok, err = repo.OpenForUser(ctx, otherID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, foreign.ID, got.ID, "other user's message is not mixed in")
}

func TestStatusMessages_notFoundAndBadIDs(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 815).ID.String())

	_, err := repo.ByID(ctx, user, "00000000-0000-7000-8000-000000000000")
	require.ErrorIs(t, err, docsbot.ErrNotFound)

	_, err = repo.ByID(ctx, user, "not-a-uuid")
	require.Error(t, err)
	_, _, err = repo.OpenForUser(ctx, "not-a-uuid")
	require.Error(t, err)
}

func TestStatusMessages_deleteUserData(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 820)
	other := makeUser(t, repo, 821)
	user, otherID := docsbot.UserID(u.ID.String()), docsbot.UserID(other.ID.String())

	sm, err := repo.Create(ctx, user, 820, 1, docsbot.StatusBatch)
	require.NoError(t, err)
	require.NoError(t, repo.Attach(ctx, user, newDoc(t, repo, u, "x1"), sm.ID))
	keep, err := repo.Create(ctx, otherID, 821, 1, docsbot.StatusBatch)
	require.NoError(t, err)
	require.NoError(t, repo.Attach(ctx, otherID, newDoc(t, repo, other, "x2"), keep.ID))

	require.NoError(t, repo.DeleteUserData(ctx, user), "documents referencing the status message do not block deletion")

	_, err = repo.ByID(ctx, user, sm.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound, "status messages deleted")
	_, ok, err := repo.OpenForUser(ctx, user)
	require.NoError(t, err)
	require.False(t, ok)

	// The other user's batch is intact.
	docs, err := repo.DocumentsIn(ctx, otherID, keep.ID)
	require.NoError(t, err)
	require.Len(t, docs, 1)
}
