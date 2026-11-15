//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestClaimFailed(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()

	mk := func(unique string, st docsbot.BackupStatus) docsbot.DocumentID {
		d := titled(t, repo, a, unique, "d")
		require.NoError(t, repo.SetBackupStatus(ctx, a, d.ID, st))
		return d.ID
	}
	failed := mk("failed", docsbot.BackupFailed)

	// Someone else cannot claim it.
	ok, err := repo.ClaimFailed(ctx, b, failed)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, docsbot.BackupFailed, backupOf(t, repo, a, failed).BackupStatus)

	// The owner claims once; the double tap gets nothing.
	ok, err = repo.ClaimFailed(ctx, a, failed)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, docsbot.BackupPending, backupOf(t, repo, a, failed).BackupStatus)
	ok, err = repo.ClaimFailed(ctx, a, failed)
	require.NoError(t, err)
	require.False(t, ok)

	for _, st := range []docsbot.BackupStatus{docsbot.BackupBackedUp, docsbot.BackupUploading, docsbot.BackupPending, docsbot.BackupNone, docsbot.BackupTooLarge} {
		id := mk("s-"+string(st), st)
		ok, err = repo.ClaimFailed(ctx, a, id)
		require.NoError(t, err)
		require.False(t, ok, "%s is not retryable", st)
		require.Equal(t, st, backupOf(t, repo, a, id).BackupStatus)
	}
}

func TestClaimFailed_parallelClaimsWinOnce(t *testing.T) {
	repo, a, _ := twoUsers(t)
	d := titled(t, repo, a, "race", "d")
	require.NoError(t, repo.SetBackupStatus(t.Context(), a, d.ID, docsbot.BackupFailed))

	wins := make(chan bool, 10)
	for range 10 {
		go func() {
			ok, err := repo.ClaimFailed(t.Context(), a, d.ID)
			if err != nil {
				ok = false
			}
			wins <- ok
		}()
	}
	n := 0
	for range 10 {
		if <-wins {
			n++
		}
	}
	require.Equal(t, 1, n)
}

func TestClaimTransferable_onlyNoneAndFailedOwnedByUser(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	want := map[docsbot.BackupStatus]bool{docsbot.BackupNone: true, docsbot.BackupFailed: true}
	byStatus := map[docsbot.BackupStatus]docsbot.DocumentID{}
	for _, st := range []docsbot.BackupStatus{
		docsbot.BackupNone, docsbot.BackupFailed, docsbot.BackupPending,
		docsbot.BackupUploading, docsbot.BackupBackedUp, docsbot.BackupTooLarge,
	} {
		d := titled(t, repo, a, "n-"+string(st), "d")
		require.NoError(t, repo.SetBackupStatus(ctx, a, d.ID, st))
		byStatus[st] = d.ID
	}
	foreign := titled(t, repo, b, "foreign", "d")

	ids, err := repo.ClaimTransferable(ctx, a)
	require.NoError(t, err)
	got := map[docsbot.DocumentID]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for st, id := range byStatus {
		require.Equal(t, want[st], got[id], "status %s", st)
	}
	require.False(t, got[foreign.ID])
	require.Equal(t, docsbot.BackupNone, backupOf(t, repo, b, foreign.ID).BackupStatus, "other user's document untouched")
	for id := range got {
		require.Equal(t, docsbot.BackupPending, backupOf(t, repo, a, id).BackupStatus)
	}

	again, err := repo.ClaimTransferable(ctx, a)
	require.NoError(t, err)
	require.Empty(t, again, "a second claim finds nothing")
}

func TestClaim_parallelClaimsPartitionDocuments(t *testing.T) {
	repo, a, _ := twoUsers(t)
	const docs = 6
	for i := range docs {
		d := titled(t, repo, a, "pc-"+string(rune('a'+i)), "d")
		if i%2 == 0 {
			require.NoError(t, repo.SetBackupStatus(t.Context(), a, d.ID, docsbot.BackupFailed))
		}
	}

	type result struct {
		ids []docsbot.DocumentID
		err error
	}
	res := make(chan result, 8)
	for range 8 {
		go func() {
			ids, err := repo.ClaimTransferable(t.Context(), a)
			res <- result{ids, err}
		}()
	}
	seen := map[docsbot.DocumentID]int{}
	for range 8 {
		r := <-res
		require.NoError(t, r.err)
		for _, id := range r.ids {
			seen[id]++
		}
	}
	require.Len(t, seen, docs, "every document is claimed")
	for id, n := range seen {
		require.Equal(t, 1, n, "document %s claimed by more than one caller", id)
	}
}

func TestClaimAllFailed_onlyFailed(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	failed := titled(t, repo, a, "f", "d")
	none := titled(t, repo, a, "n", "d")
	foreign := titled(t, repo, b, "bf", "d")
	require.NoError(t, repo.SetBackupStatus(ctx, a, failed.ID, docsbot.BackupFailed))
	require.NoError(t, repo.SetBackupStatus(ctx, b, foreign.ID, docsbot.BackupFailed))

	ids, err := repo.ClaimAllFailed(ctx, a)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{failed.ID}, ids)
	require.Equal(t, docsbot.BackupNone, backupOf(t, repo, a, none.ID).BackupStatus)
	require.Equal(t, docsbot.BackupFailed, backupOf(t, repo, b, foreign.ID).BackupStatus)

	again, err := repo.ClaimAllFailed(ctx, a)
	require.NoError(t, err)
	require.Empty(t, again)
}
