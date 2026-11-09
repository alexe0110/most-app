//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

func albumDoc(t *testing.T, repo *postgres.Repo, user docsbot.UserID, group, unique string) docsbot.Document {
	t.Helper()
	doc, err := repo.CreateDocument(t.Context(), docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindPhoto, FileID: "f-" + unique, FileUniqueID: unique, MediaGroupID: group,
	})
	require.NoError(t, err)
	return doc
}

func TestDocuments_mediaGroupListsOnlyOwnPendingOfAlbum(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	p1 := albumDoc(t, repo, a, "g1", "p1")
	p2 := albumDoc(t, repo, a, "g1", "p2")
	saved := albumDoc(t, repo, a, "g1", "p3")
	require.NoError(t, repo.SaveDocument(ctx, a, saved.ID, docsbot.CatOther))
	albumDoc(t, repo, a, "g2", "q1")
	albumDoc(t, repo, b, "g1", "foreign")
	single, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: a, Kind: docsbot.KindPhoto, FileID: "s", FileUniqueID: "s"})
	require.NoError(t, err)
	require.Empty(t, single.MediaGroupID)
	require.Equal(t, "g1", backupOf(t, repo, a, p1.ID).MediaGroupID)

	got, err := repo.PendingByMediaGroup(ctx, a, "g1")
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{p1.ID, p2.ID}, []docsbot.DocumentID{got[0].ID, got[1].ID}, "arrival order, pending only")
	require.Len(t, got, 2)

	for _, d := range got {
		require.NoError(t, repo.SaveDocument(ctx, a, d.ID, docsbot.CatReceipts))
	}
	rest, err := repo.PendingByMediaGroup(ctx, a, "g1")
	require.NoError(t, err)
	require.Empty(t, rest)
	other, err := repo.PendingByMediaGroup(ctx, a, "g2")
	require.NoError(t, err)
	require.Len(t, other, 1, "another album is untouched")
	foreign, err := repo.PendingByMediaGroup(ctx, b, "g1")
	require.NoError(t, err)
	require.Len(t, foreign, 1, "another user's album is untouched")
}

func TestDocuments_intakeStats(t *testing.T) {
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	a := docsbot.UserID(makeUser(t, repo, 910).ID.String())
	b := docsbot.UserID(makeUser(t, repo, 911).ID.String())
	ctx := t.Context()

	p1 := albumDoc(t, repo, a, "", "p1")
	albumDoc(t, repo, a, "", "p2")
	s1 := albumDoc(t, repo, a, "", "s1")
	s2 := albumDoc(t, repo, a, "", "s2")
	require.NoError(t, repo.SaveDocument(ctx, a, s1.ID, docsbot.CatOther))
	require.NoError(t, repo.SaveDocument(ctx, a, s2.ID, docsbot.CatOther))
	albumDoc(t, repo, b, "", "foreign")
	_, err := pool.Exec(ctx, `update documents set created_at = now() - interval '5 minutes' where id in ($1::uuid, $2::uuid)`,
		string(p1.ID), string(s2.ID))
	require.NoError(t, err)

	st, err := repo.IntakeStats(ctx, a, time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.Equal(t, docsbot.IntakeStats{Pending: 2, Recent: 2, Total: 4}, st)

	empty, err := repo.IntakeStats(ctx, docsbot.UserID("00000000-0000-0000-0000-000000000000"), time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.Equal(t, docsbot.IntakeStats{}, empty)
}

func TestDocuments_purgeStalePending(t *testing.T) {
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	a := docsbot.UserID(makeUser(t, repo, 920).ID.String())
	b := docsbot.UserID(makeUser(t, repo, 921).ID.String())
	ctx := t.Context()

	stale := albumDoc(t, repo, a, "", "stale")
	staleOther := albumDoc(t, repo, b, "", "stale-b")
	fresh := albumDoc(t, repo, a, "", "fresh")
	oldSaved := albumDoc(t, repo, a, "", "old-saved")
	require.NoError(t, repo.SaveDocument(ctx, a, oldSaved.ID, docsbot.CatOther))
	_, err := pool.Exec(ctx, `update documents set created_at = now() - interval '25 hours' where id in ($1::uuid, $2::uuid, $3::uuid)`,
		string(stale.ID), string(staleOther.ID), string(oldSaved.ID))
	require.NoError(t, err)

	n, err := repo.PurgeStalePending(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, n, "stale pending of every user")

	_, err = repo.DocumentByID(ctx, a, stale.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = repo.DocumentByID(ctx, b, staleOther.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	backupOf(t, repo, a, fresh.ID)
	backupOf(t, repo, a, oldSaved.ID)
}

func TestDocuments_notBackedUpExcludesTooLarge(t *testing.T) {
	repo, user, _ := twoUsers(t)
	ctx := t.Context()
	plain := titled(t, repo, user, "plain", "plain")
	big := titled(t, repo, user, "big", "big")
	require.NoError(t, repo.SetBackupStatus(ctx, user, big.ID, docsbot.BackupTooLarge))

	n, err := repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	ids, err := repo.ClaimTransferable(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{plain.ID}, ids, "a too_large document is never claimed for transfer")
}

func TestDocuments_duplicateAfterDeleteCreatesNewDocument(t *testing.T) {
	repo, user, _ := twoUsers(t)
	ctx := t.Context()
	in := docsbot.IncomingFile{UserID: user, Kind: docsbot.KindDocument, FileID: "f", FileUniqueID: "same"}

	first, err := repo.CreateDocument(ctx, in)
	require.NoError(t, err)
	_, err = repo.CreateDocument(ctx, in)
	require.Error(t, err, "unique(user_id, tg_file_unique_id) rejects a live duplicate")

	require.NoError(t, repo.DeleteDocument(ctx, user, first.ID))
	again, err := repo.CreateDocument(ctx, in)
	require.NoError(t, err, "deletion is hard, so the same file can be stored again")
	require.NotEqual(t, first.ID, again.ID)
}
