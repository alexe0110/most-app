package docsbot_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: tt.owner, Status: docsbot.StatusSaved, BackupStatus: tt.status})
			svc, uploads := edgeService(repo, fakeDrive{connected: true})

			err := svc.RetryBackup(t.Context(), user, doc.ID)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Empty(t, uploads.requeued)
				require.Equal(t, tt.status, repo.docs[doc.ID].BackupStatus, "state must stay untouched")
				return
			}
			require.NoError(t, err)
			require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.requeued)
			require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
		})
	}
}

func TestService_RetryBackup_doubleTapQueuesOnce(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	require.NoError(t, svc.RetryBackup(t.Context(), user, doc.ID))
	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
	require.Len(t, uploads.requeued, 1)
}

func TestService_RetryBackup_parallelTapsQueueOnce(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

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
	require.Len(t, uploads.requeued, 1)
}

func TestService_TransferSaved_secondTapHasNothingToTransfer(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupPending})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupUploading})
	svc, uploads := edgeService(repo, fakeDrive{connected: true})

	view, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2, "only none and failed are transferred")

	_, ok, err = svc.TransferSaved(t.Context(), user, 5, 51)
	require.NoError(t, err)
	require.False(t, ok, "the second tap must not open a second status message")
	require.Len(t, uploads.requeued, 2)
}

func TestService_parallelTransferSaved_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	for range 5 {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	}
	status := newFakeStatus(repo)
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, status, &fakeSyncs{repo: repo})

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
	require.Len(t, status.msgs, 1, "and only one status message exists")
	require.Len(t, uploads.requeued, 5, "each document is queued once")
	for _, d := range repo.docs {
		require.Equal(t, docsbot.BackupPending, d.BackupStatus)
	}
}

func TestService_parallelRetryFailed_exactlyOnce(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	for range 3 {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	}
	status := newFakeStatus(repo)
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, uploads, repo, status, &fakeSyncs{repo: repo})

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
	require.Len(t, status.msgs, 1)
	require.Len(t, uploads.requeued, 3)
}

func TestService_TransferSaved(t *testing.T) {
	repo := newFakeRepo()
	a := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupBackedUp})
	b := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending})
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	view, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2)
	require.Equal(t, docsbot.StatusTransfer, view.Message.Kind)
	require.Equal(t, 50, view.Message.MessageID)
	require.Equal(t, view.Message.ID, repo.docs[a.ID].StatusMessageID)
	require.Equal(t, view.Message.ID, repo.docs[b.ID].StatusMessageID)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, uploads.requeued)
}

func TestService_TransferSaved_nothingToDo(t *testing.T) {
	repo := newFakeRepo()
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, uploads.requeued)
}

func TestService_RetryFailed(t *testing.T) {
	repo := newFakeRepo()
	a := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	b := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	view, ok, err := svc.RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2)
	require.Equal(t, docsbot.StatusTransfer, view.Message.Kind)
	require.Equal(t, view.Message.ID, repo.docs[a.ID].StatusMessageID)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, uploads.requeued)

	empty := newFakeRepo()
	_, ok, err = docsbot.NewService(empty, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: empty}, empty, newFakeStatus(empty), &fakeSyncs{repo: empty}).
		RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
}
