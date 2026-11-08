package docsbot_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_RetryStatusFailed(t *testing.T) {
	ctx := context.Background()
	setup := func() (*docsbot.Service, *fakeUploads, *fakeRepo, docsbot.StatusMessageID, map[string]docsbot.DocumentID) {
		repo := newFakeRepo()
		status := newFakeStatus(repo)
		sm, err := status.Create(ctx, user, 7, 70)
		require.NoError(t, err)
		ids := map[string]docsbot.DocumentID{}
		for name, st := range map[string]docsbot.BackupStatus{
			"failed1": docsbot.BackupFailed, "failed2": docsbot.BackupFailed,
			"ok": docsbot.BackupBackedUp, "big": docsbot.BackupTooLarge, "busy": docsbot.BackupUploading,
		} {
			ids[name] = repo.seed(docsbot.Document{UserID: user, BackupStatus: st, StatusMessageID: sm.ID}).ID
		}
		up := &fakeUploads{repo: repo}
		return docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, up, repo, status), up, repo, sm.ID, ids
	}

	t.Run("requeues only failed and flips them back to pending", func(t *testing.T) {
		svc, up, repo, smID, ids := setup()

		n, err := svc.RetryStatusFailed(ctx, user, smID)
		require.NoError(t, err)
		require.Equal(t, 2, n)
		require.ElementsMatch(t, []docsbot.DocumentID{ids["failed1"], ids["failed2"]}, up.calls)
		require.Equal(t, docsbot.BackupPending, repo.docs[ids["failed1"]].BackupStatus)
		require.Equal(t, docsbot.BackupBackedUp, repo.docs[ids["ok"]].BackupStatus)
		require.Equal(t, docsbot.BackupTooLarge, repo.docs[ids["big"]].BackupStatus)

		v, err := svc.StatusViewByID(ctx, user, smID)
		require.NoError(t, err)
		require.False(t, v.AllTerminal, "retried lines are in progress again")
	})

	t.Run("another user's message is not found and nothing is queued", func(t *testing.T) {
		svc, up, _, smID, _ := setup()

		_, err := svc.RetryStatusFailed(ctx, "intruder", smID)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		require.Empty(t, up.calls)

		_, err = svc.StatusViewByID(ctx, "intruder", smID)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	})

	t.Run("unknown message", func(t *testing.T) {
		svc, up, _, _, _ := setup()

		_, err := svc.RetryStatusFailed(ctx, user, "nope")
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		require.Empty(t, up.calls)
	})
}

func TestService_OpenAndAttachStatus(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	status := newFakeStatus(repo)
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, status)
	a := repo.seed(docsbot.Document{UserID: user})
	b := repo.seed(docsbot.Document{UserID: user})

	sm, err := svc.OpenStatus(ctx, user, 9, 90, a.ID)
	require.NoError(t, err)
	require.NoError(t, svc.AttachStatus(ctx, b.ID, sm.ID))

	v, err := svc.StatusViewByID(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Len(t, v.Lines, 2)

	require.NoError(t, svc.SetStatusMessageID(ctx, sm.ID, 91))
	require.NoError(t, svc.CloseStatus(ctx, sm.ID))
	_, ok, err := svc.StatusTarget(ctx, user)
	require.NoError(t, err)
	require.False(t, ok, "closed message is not reused")
}
