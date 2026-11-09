package docsbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// errRequeueUploads is fakeUploads whose RequeueUpload always fails.
type errRequeueUploads struct {
	*fakeUploads
	err error
}

func (f errRequeueUploads) RequeueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return f.err
}

// A claim that succeeded but whose requeue failed leaves the document pending
// (the sweeper requeues it): consistent, and a second tap is a quiet no-op.
func TestService_RetryBackup_requeueFailureLeavesPending(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	boom := errors.New("queue down")
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{},
		errRequeueUploads{&fakeUploads{repo: repo}, boom}, repo, newFakeStatus(repo))

	err := svc.RetryBackup(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, docsbot.ErrNotRetryable)
	require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)

	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
}

func TestService_TransferSaved_requeueFailureKeepsDocsPending(t *testing.T) {
	repo := newFakeRepo()
	a := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	b := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	boom := errors.New("queue down")
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{},
		errRequeueUploads{&fakeUploads{repo: repo}, boom}, repo, newFakeStatus(repo))

	_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)

	require.ErrorIs(t, err, boom)
	require.False(t, ok)
	for _, id := range []docsbot.DocumentID{a.ID, b.ID} {
		require.Equal(t, docsbot.BackupPending, repo.docs[id].BackupStatus, "claimed docs stay pending for the sweeper")
	}
}

func TestService_Search_hugeAndOddQueries(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})
	for _, q := range []string{"%", "_", `\`, "😀😀", "ДОГОВОР"} {
		found, err := svc.Search(t.Context(), user, q)
		require.NoError(t, err, q)
		require.False(t, found.BlankQuery, q)
	}
}
