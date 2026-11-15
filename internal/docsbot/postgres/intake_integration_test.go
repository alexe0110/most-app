//go:build integration

package postgres_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

func albumDoc(t *testing.T, repo *adapters, user docsbot.UserID, group, unique string) docsbot.Document {
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
	mustSave(t, repo, a, saved.ID, docsbot.CatOther)
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
		mustSave(t, repo, a, d.ID, docsbot.CatReceipts)
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

func TestDocuments_numberPendingAlbum(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	p1 := albumDoc(t, repo, a, "g1", "p1")
	p2 := albumDoc(t, repo, a, "g1", "p2")
	saved := albumDoc(t, repo, a, "g1", "p3")
	mustSave(t, repo, a, saved.ID, docsbot.CatOther)
	p4 := albumDoc(t, repo, a, "g1", "p4")
	other := albumDoc(t, repo, a, "g2", "q1")
	foreign := albumDoc(t, repo, b, "g1", "foreign")
	titleOf := func(user docsbot.UserID, id docsbot.DocumentID) string {
		t.Helper()
		doc, err := repo.DocumentByID(ctx, user, id)
		require.NoError(t, err)
		return doc.Title
	}
	savedTitle := titleOf(a, saved.ID)

	require.NoError(t, repo.NumberPendingAlbum(ctx, a, "g1", "Отпуск"))

	require.Equal(t, "Отпуск (1)", titleOf(a, p1.ID), "arrival order")
	require.Equal(t, "Отпуск (2)", titleOf(a, p2.ID))
	require.Equal(t, "Отпуск (3)", titleOf(a, p4.ID), "numbers skip the saved document")
	require.Equal(t, savedTitle, titleOf(a, saved.ID), "a saved document is untouched")
	require.Empty(t, titleOf(a, other.ID), "another album is untouched")
	require.Empty(t, titleOf(b, foreign.ID), "another user's album is untouched")
}

func TestDocuments_intakeStats(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	a := docsbot.UserID(makeUser(t, repo, 910).ID.String())
	b := docsbot.UserID(makeUser(t, repo, 911).ID.String())
	ctx := t.Context()

	p1 := albumDoc(t, repo, a, "", "p1")
	albumDoc(t, repo, a, "", "p2")
	s1 := albumDoc(t, repo, a, "", "s1")
	s2 := albumDoc(t, repo, a, "", "s2")
	mustSave(t, repo, a, s1.ID, docsbot.CatOther)
	mustSave(t, repo, a, s2.ID, docsbot.CatOther)
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
	repo := newAdapters(pool, noJobs{})
	a := docsbot.UserID(makeUser(t, repo, 920).ID.String())
	b := docsbot.UserID(makeUser(t, repo, 921).ID.String())
	ctx := t.Context()

	stale := albumDoc(t, repo, a, "", "stale")
	staleOther := albumDoc(t, repo, b, "", "stale-b")
	fresh := albumDoc(t, repo, a, "", "fresh")
	oldSaved := albumDoc(t, repo, a, "", "old-saved")
	mustSave(t, repo, a, oldSaved.ID, docsbot.CatOther)
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
	require.ErrorIs(t, err, docsbot.ErrDuplicateFile, "unique(user_id, tg_file_unique_id) rejects a live duplicate")

	_, err = repo.DeleteDocument(ctx, user, first.ID, false)
	require.NoError(t, err)
	again, err := repo.CreateDocument(ctx, in)
	require.NoError(t, err, "deletion is hard, so the same file can be stored again")
	require.NotEqual(t, first.ID, again.ID)
}

// Parallel sends of one file (a double tap, a forwarded album echo) race on the
// insert: exactly one document may result and every call must answer by it.
func TestReceive_parallelSendsOfOneFileCreateOneDocument(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	user := docsbot.UserID(makeUser(t, repo, 930).ID.String())
	svc := docsbot.NewService(docsbot.Ports{Repo: repo.Documents, Events: docsbottest.NoEvents{}})
	ctx := t.Context()

	const senders = 8
	sendAll := func(unique string) []docsbot.Reply {
		replies := make([]docsbot.Reply, senders)
		errs := make([]error, senders)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range senders {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				replies[i], errs[i] = svc.Receive(ctx, docsbot.IncomingFile{
					UserID: user, Kind: docsbot.KindDocument, FileID: "f-" + unique, FileUniqueID: unique,
				})
			}()
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}
		return replies
	}
	count := func(unique string) (n int) {
		require.NoError(t, pool.QueryRow(ctx,
			`select count(*) from documents where user_id = $1::uuid and tg_file_unique_id = $2`, string(user), unique).Scan(&n))
		return n
	}

	for _, unique := range []string{"par-1", "par-2", "par-3", "par-4", "par-5"} {
		replies := sendAll(unique)
		require.Equal(t, 1, count(unique))
		var id docsbot.DocumentID
		for _, rep := range replies {
			need, ok := rep.(docsbot.NeedCategory)
			require.True(t, ok, "every sender is asked for the category")
			if id == "" {
				id = need.Doc.ID
			}
			require.Equal(t, id, need.Doc.ID, "all replies refer to the one document")
		}

		mustSave(t, repo, user, id, docsbot.CatOther)
		for _, rep := range sendAll(unique) {
			already, ok := rep.(docsbot.AlreadySaved)
			require.True(t, ok, "a saved file answers AlreadySaved")
			require.Equal(t, id, already.Doc.ID)
		}
		require.Equal(t, 1, count(unique))
	}
}

// Discarding an album after merging it must remove pending photos only and need
// no job: a pending document has no Drive copy.
func TestService_discardAlbumDeletesPendingOnly(t *testing.T) {
	repo, a, b := twoUsers(t)
	svc := docsbot.NewService(docsbot.Ports{Repo: repo.Documents, Syncs: repo.Documents, Events: docsbottest.NoEvents{}})
	ctx := t.Context()
	p1 := albumDoc(t, repo, a, "g1", "p1")
	p2 := albumDoc(t, repo, a, "g1", "p2")
	saved := albumDoc(t, repo, a, "g1", "p3")
	mustSave(t, repo, a, saved.ID, docsbot.CatOther)
	otherAlbum := albumDoc(t, repo, a, "g2", "q1")
	foreign := albumDoc(t, repo, b, "g1", "foreign")

	n, err := svc.DiscardAlbum(ctx, a, "g1")

	require.NoError(t, err, "noJobs fails the test if a job was requested")
	require.Equal(t, 2, n)
	for _, id := range []docsbot.DocumentID{p1.ID, p2.ID} {
		_, err := repo.DocumentByID(ctx, a, id)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	}
	backupOf(t, repo, a, saved.ID)
	backupOf(t, repo, a, otherAlbum.ID)
	backupOf(t, repo, b, foreign.ID)

	n, err = svc.DiscardAlbum(ctx, a, "g1")
	require.NoError(t, err)
	require.Zero(t, n, "second call finds nothing")
	n, err = svc.DiscardAlbum(ctx, a, "")
	require.NoError(t, err)
	require.Zero(t, n, "no album id deletes nothing")
}
