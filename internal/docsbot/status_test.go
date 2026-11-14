package docsbot_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_StatusTarget(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	tests := []struct {
		name   string
		msg    *docsbot.StatusMessage
		reuse  bool
		wantID docsbot.StatusMessageID
	}{
		{name: "fresh open message", msg: &docsbot.StatusMessage{ID: "sm-a", UserID: user, CreatedAt: now.Add(-29 * time.Second)}, reuse: true, wantID: "sm-a"},
		{name: "age equals window", msg: &docsbot.StatusMessage{ID: "sm-a", UserID: user, CreatedAt: now.Add(-30 * time.Second)}},
		{name: "older than window", msg: &docsbot.StatusMessage{ID: "sm-a", UserID: user, CreatedAt: now.Add(-time.Minute)}},
		{name: "closed", msg: &docsbot.StatusMessage{ID: "sm-a", UserID: user, CreatedAt: now.Add(-time.Second), Closed: true}},
		{name: "none", msg: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			status := docsbottest.NewFakeStatus(repo)
			if tt.msg != nil {
				status.Msgs[tt.msg.ID] = *tt.msg
			}
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, status, &docsbottest.FakeSyncs{Repo: repo}, docsbot.WithClock(func() time.Time { return now }))

			sm, ok, err := svc.StatusTarget(context.Background(), user)
			require.NoError(t, err)
			require.Equal(t, tt.reuse, ok)
			require.Equal(t, tt.wantID, sm.ID)
		})
	}
}

func TestService_StatusView(t *testing.T) {
	t.Run("not grouped", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		doc := repo.Seed(docsbot.Document{UserID: user})
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		v, err := svc.StatusView(context.Background(), user, doc.ID)
		require.NoError(t, err)
		require.False(t, v.Grouped)
		require.Equal(t, doc.ID, v.Doc.ID)
	})

	tests := []struct {
		name        string
		second      docsbot.BackupStatus
		allTerminal bool
	}{
		{name: "all terminal", second: docsbot.BackupFailed, allTerminal: true},
		{name: "one uploading", second: docsbot.BackupUploading, allTerminal: false},
	}
	for _, tt := range tests {
		t.Run("grouped "+tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			repo.Folders = map[docsbot.Category]string{docsbot.CatPassport: "folder-p"}
			status := docsbottest.NewFakeStatus(repo)
			sm, err := status.Create(context.Background(), user, 7, 70, docsbot.StatusBatch)
			require.NoError(t, err)
			a := repo.Seed(docsbot.Document{UserID: user, Category: docsbot.CatPassport, BackupStatus: docsbot.BackupBackedUp, StatusMessageID: sm.ID})
			b := repo.Seed(docsbot.Document{UserID: user, Category: docsbot.CatAuto, BackupStatus: tt.second, StatusMessageID: sm.ID})
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, status, &docsbottest.FakeSyncs{Repo: repo})

			v, err := svc.StatusView(context.Background(), user, a.ID)
			require.NoError(t, err)
			require.True(t, v.Grouped)
			require.Equal(t, sm.ID, v.Message.ID)
			require.Len(t, v.Lines, 2)
			require.ElementsMatch(t, []docsbot.DocumentID{a.ID, b.ID}, []docsbot.DocumentID{v.Lines[0].ID, v.Lines[1].ID})
			require.Equal(t, map[docsbot.Category]string{docsbot.CatPassport: "folder-p"}, v.FolderByCat)
			require.Equal(t, tt.allTerminal, v.AllTerminal)
		})
	}
}

func TestService_RetryStatusFailed(t *testing.T) {
	ctx := context.Background()
	setup := func() (*docsbot.Service, *docsbottest.FakeUploads, *docsbottest.FakeRepo, docsbot.StatusMessageID, map[string]docsbot.DocumentID) {
		repo := docsbottest.NewFakeRepo()
		status := docsbottest.NewFakeStatus(repo)
		sm, err := status.Create(ctx, user, 7, 70, docsbot.StatusBatch)
		require.NoError(t, err)
		ids := map[string]docsbot.DocumentID{}
		for name, st := range map[string]docsbot.BackupStatus{
			"failed1": docsbot.BackupFailed, "failed2": docsbot.BackupFailed,
			"ok": docsbot.BackupBackedUp, "big": docsbot.BackupTooLarge, "busy": docsbot.BackupUploading,
		} {
			ids[name] = repo.Seed(docsbot.Document{UserID: user, BackupStatus: st, StatusMessageID: sm.ID}).ID
		}
		up := &docsbottest.FakeUploads{Repo: repo}
		return docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, up, repo, status, &docsbottest.FakeSyncs{Repo: repo}), up, repo, sm.ID, ids
	}

	t.Run("requeues only failed and flips them back to pending", func(t *testing.T) {
		svc, up, repo, smID, ids := setup()

		n, err := svc.RetryStatusFailed(ctx, user, smID)
		require.NoError(t, err)
		require.Equal(t, 2, n)
		require.ElementsMatch(t, []docsbot.DocumentID{ids["failed1"], ids["failed2"]}, up.Requeued)
		require.Equal(t, docsbot.BackupPending, repo.Docs[ids["failed1"]].BackupStatus)
		require.Equal(t, docsbot.BackupBackedUp, repo.Docs[ids["ok"]].BackupStatus)
		require.Equal(t, docsbot.BackupTooLarge, repo.Docs[ids["big"]].BackupStatus)

		v, err := svc.StatusViewByID(ctx, user, smID)
		require.NoError(t, err)
		require.False(t, v.AllTerminal, "retried lines are in progress again")
	})

	t.Run("another user's message is not found and nothing is queued", func(t *testing.T) {
		svc, up, _, smID, _ := setup()

		_, err := svc.RetryStatusFailed(ctx, "intruder", smID)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		require.Empty(t, up.Calls)

		_, err = svc.StatusViewByID(ctx, "intruder", smID)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	})

	t.Run("unknown message", func(t *testing.T) {
		svc, up, _, _, _ := setup()

		_, err := svc.RetryStatusFailed(ctx, user, "nope")
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		require.Empty(t, up.Calls)
	})
}

func TestService_OpenAndAttachStatus(t *testing.T) {
	ctx := context.Background()
	repo := docsbottest.NewFakeRepo()
	status := docsbottest.NewFakeStatus(repo)
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, status, &docsbottest.FakeSyncs{Repo: repo})
	a := repo.Seed(docsbot.Document{UserID: user})
	b := repo.Seed(docsbot.Document{UserID: user})

	sm, err := svc.OpenStatus(ctx, user, 9, 90, a.ID, docsbot.StatusSingle)
	require.NoError(t, err)
	require.NoError(t, svc.AttachStatus(ctx, user, b.ID, sm.ID))

	v, err := svc.StatusViewByID(ctx, user, sm.ID)
	require.NoError(t, err)
	require.Len(t, v.Lines, 2)

	require.NoError(t, svc.SetStatusMessageID(ctx, user, sm.ID, 91))
	require.NoError(t, svc.CloseStatus(ctx, user, sm.ID))
	_, ok, err := svc.StatusTarget(ctx, user)
	require.NoError(t, err)
	require.False(t, ok, "closed message is not reused")
}
