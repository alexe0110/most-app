package docsbot_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

// errRequeueUploads is docsbottest.FakeUploads whose RequeueUpload always fails.
type errRequeueUploads struct {
	*docsbottest.FakeUploads
	err error
}

func (f errRequeueUploads) RequeueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return f.err
}

// A claim that succeeded but whose requeue failed leaves the document pending
// (the sweeper requeues it): consistent, and a second tap is a quiet no-op.
func TestService_RetryBackup_requeueFailureLeavesPending(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	boom := errors.New("queue down")
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, errRequeueUploads{&docsbottest.FakeUploads{Repo: repo}, boom}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	err := svc.RetryBackup(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, docsbot.ErrNotRetryable)
	require.Equal(t, docsbot.BackupPending, repo.Docs[doc.ID].BackupStatus)

	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
}

func TestService_TransferSaved_requeueFailureKeepsDocsPending(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	a := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	b := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	boom := errors.New("queue down")
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, errRequeueUploads{&docsbottest.FakeUploads{Repo: repo}, boom}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)

	require.ErrorIs(t, err, boom)
	require.False(t, ok)
	for _, id := range []docsbot.DocumentID{a.ID, b.ID} {
		require.Equal(t, docsbot.BackupPending, repo.Docs[id].BackupStatus, "claimed docs stay pending for the sweeper")
	}
}

func TestService_RetryBackup_onlyFromFailed(t *testing.T) {
	tests := []struct {
		name    string
		status  docsbot.BackupStatus
		owner   docsbot.UserID
		wantErr error
	}{
		{"failed is retried", docsbot.BackupFailed, user, nil},
		{"backed up", docsbot.BackupBackedUp, user, docsbot.ErrNotRetryable},
		{"uploading", docsbot.BackupUploading, user, docsbot.ErrNotRetryable},
		{"pending", docsbot.BackupPending, user, docsbot.ErrNotRetryable},
		{"none", docsbot.BackupNone, user, docsbot.ErrNotRetryable},
		{"too large", docsbot.BackupTooLarge, user, docsbot.ErrNotRetryable},
		{"someone else's failed document", docsbot.BackupFailed, stranger, docsbot.ErrNotRetryable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			doc := repo.Seed(docsbot.Document{UserID: tt.owner, Status: docsbot.StatusSaved, BackupStatus: tt.status})
			svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

			err := svc.RetryBackup(t.Context(), user, doc.ID)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, uploads.Requeued)
				require.Equal(t, tt.status, repo.Docs[doc.ID].BackupStatus, "state must stay untouched")
				return
			}
			require.NoError(t, err)
			require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.Requeued)
			require.Equal(t, docsbot.BackupPending, repo.Docs[doc.ID].BackupStatus)
		})
	}
}

func TestService_RetryBackup_doubleTapQueuesOnce(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	require.NoError(t, svc.RetryBackup(t.Context(), user, doc.ID))
	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
	require.Len(t, uploads.Requeued, 1)
}

func TestService_RetryBackup_parallelTapsQueueOnce(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	errs := runParallel(20, func(int) error { return svc.RetryBackup(t.Context(), user, doc.ID) })

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, docsbot.ErrNotRetryable)
	}
	require.Equal(t, 1, ok)
	require.Len(t, uploads.Requeued, 1)
}

func TestService_TransferSaved_secondTapHasNothingToTransfer(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupPending})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupUploading})
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{IsConnected: true})

	view, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2, "only none and failed are transferred")

	_, ok, err = svc.TransferSaved(t.Context(), user, 5, 51)
	require.NoError(t, err)
	require.False(t, ok, "the second tap must not open a second status message")
	require.Len(t, uploads.Requeued, 2)
}

func TestService_parallelTransferSaved_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := docsbottest.NewFakeRepo()
	for range 5 {
		repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	}
	status := docsbottest.NewFakeStatus(repo)
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, uploads, repo, status, &docsbottest.FakeSyncs{Repo: repo})

	var mu sync.Mutex
	started := 0
	errs := runParallel(10, func(i int) error {
		_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50+i)
		mu.Lock()
		defer mu.Unlock()
		if ok {
			started++
		}
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, started, "exactly one tap starts the transfer")
	require.Len(t, status.Msgs, 1, "and only one status message exists")
	require.Len(t, uploads.Requeued, 5, "each document is queued once")
	for _, d := range repo.Docs {
		require.Equal(t, docsbot.BackupPending, d.BackupStatus)
	}
}

func TestService_parallelRetryFailed_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := docsbottest.NewFakeRepo()
	for range 3 {
		repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	}
	status := docsbottest.NewFakeStatus(repo)
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, uploads, repo, status, &docsbottest.FakeSyncs{Repo: repo})

	var mu sync.Mutex
	started := 0
	runParallel(10, func(i int) error {
		_, ok, err := svc.RetryFailed(t.Context(), user, 5, 50+i)
		mu.Lock()
		defer mu.Unlock()
		if ok {
			started++
		}
		return err
	})

	require.Equal(t, 1, started)
	require.Len(t, status.Msgs, 1)
	require.Len(t, uploads.Requeued, 3)
}

func TestService_TransferSaved(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	a := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupBackedUp})
	b := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending})
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	view, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2)
	require.Equal(t, docsbot.StatusTransfer, view.Message.Kind)
	require.Equal(t, 50, view.Message.MessageID)
	require.Equal(t, view.Message.ID, repo.Docs[a.ID].StatusMessageID)
	require.Equal(t, view.Message.ID, repo.Docs[b.ID].StatusMessageID)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, uploads.Requeued)
}

func TestService_TransferSaved_nothingToDo(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, uploads.Requeued)
}

func TestService_RetryFailed(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	a := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	b := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	view, ok, err := svc.RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2)
	require.Equal(t, docsbot.StatusTransfer, view.Message.Kind)
	require.Equal(t, view.Message.ID, repo.Docs[a.ID].StatusMessageID)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, uploads.Requeued)

	empty := docsbottest.NewFakeRepo()
	_, ok, err = docsbottest.NewService(empty, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: empty}, empty, docsbottest.NewFakeStatus(empty), &docsbottest.FakeSyncs{Repo: empty}).
		RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
}
