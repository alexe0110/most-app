package docsbot_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
