package docsbot_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_DeleteAllData_revokeFailureDoesNotBlockDeletion(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	boom := errors.New("google unreachable")
	svc, uploads := edgeService(repo, docsbottest.FakeDrive{RevokeErr: boom})

	err := svc.DeleteAllData(t.Context(), user)

	require.ErrorIs(t, err, docsbot.ErrRevokeIncomplete)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []docsbot.UserID{user}, uploads.AllJobs)
	require.Equal(t, []docsbot.UserID{user}, repo.Deleted, "data must be deleted despite the revoke failure")
	require.Empty(t, repo.Docs)
}

func TestService_Settings(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	for i := 0; i < 3; i++ {
		repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	view, err := svc.Settings(t.Context(), user)
	require.NoError(t, err)
	require.Equal(t, docsbot.SettingsView{
		DriveConnected: true, Total: 6, InDrive: 2, OnlyTelegram: 3, Failed: 1, InlineEnabled: true,
	}, view)
}

func TestService_ResetDriveFolders(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Folders = map[docsbot.Category]string{"passport": "f1", "insurance": "f2"}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	require.NoError(t, svc.ResetDriveFolders(t.Context(), user))
	require.Empty(t, repo.Folders)
}

func TestService_Disconnect(t *testing.T) {
	t.Run("disconnects, cancels uploads and resets stuck documents", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		var disconnected, revoked bool
		pending := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploading := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
		failed := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
		other := repo.Seed(docsbot.Document{UserID: "other", Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploads := &docsbottest.FakeUploads{Repo: repo}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{Disconnected: &disconnected, Revoked: &revoked}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		require.NoError(t, svc.Disconnect(t.Context(), user))
		require.True(t, disconnected)
		require.False(t, revoked, "user disconnect must not use Revoke")
		require.Equal(t, []docsbot.UserID{user}, uploads.Cancels)
		require.Empty(t, uploads.AllJobs, "sync and trash jobs stay on disconnect")
		require.Equal(t, docsbot.BackupNone, repo.Docs[pending.ID].BackupStatus)
		require.Equal(t, docsbot.BackupNone, repo.Docs[uploading.ID].BackupStatus)
		require.Equal(t, docsbot.BackupFailed, repo.Docs[failed.ID].BackupStatus)
		require.Equal(t, docsbot.BackupPending, repo.Docs[other.ID].BackupStatus, "other users untouched")
	})
	t.Run("drive failure is a hard error and cancels nothing", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		pending := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploads := &docsbottest.FakeUploads{Repo: repo}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{DisconnectErr: errors.New("boom")}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		err := svc.Disconnect(t.Context(), user)
		require.Error(t, err)
		require.NotErrorIs(t, err, docsbot.ErrRevokeIncomplete)
		require.Empty(t, uploads.Cancels, "the integration is still active")
		require.Equal(t, docsbot.BackupPending, repo.Docs[pending.ID].BackupStatus)
	})
	// Criterion 2: disconnect keeps stored documents — it must not delete them,
	// clear a backed-up doc's drive_file_id or change its backup_status.
	t.Run("leaves stored documents untouched", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		backed := repo.Seed(docsbot.Document{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport,
			BackupStatus: docsbot.BackupBackedUp, DriveFileID: "drv-1",
		})
		onlyTg := repo.Seed(docsbot.Document{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
			BackupStatus: docsbot.BackupNone,
		})
		uploads := &docsbottest.FakeUploads{Repo: repo}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		require.NoError(t, svc.Disconnect(t.Context(), user))

		require.Empty(t, repo.Deleted, "disconnect must not delete user data")
		require.Len(t, repo.Docs, 2, "no documents removed")
		require.Equal(t, docsbot.BackupBackedUp, repo.Docs[backed.ID].BackupStatus, "backed-up status kept")
		require.Equal(t, "drv-1", repo.Docs[backed.ID].DriveFileID, "drive_file_id kept")
		require.Equal(t, docsbot.BackupNone, repo.Docs[onlyTg.ID].BackupStatus, "telegram-only doc unchanged")
	})
}

func TestService_DeleteAllData(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	var revoked bool
	uploads := &docsbottest.FakeUploads{Repo: repo}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{Revoked: &revoked}, docsbottest.FakeFiles{}, uploads, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	require.NoError(t, svc.DeleteAllData(t.Context(), user))
	require.True(t, revoked)
	require.Equal(t, []docsbot.UserID{user}, uploads.AllJobs, "every pending job goes with the data")
	require.Empty(t, uploads.Cancels)
	require.Equal(t, []docsbot.UserID{user}, repo.Deleted)
	require.Empty(t, repo.Docs)
}

func TestService_ExportAll(t *testing.T) {
	t.Run("not truncated", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		for i := 0; i < 3; i++ {
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
		}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		exp, err := svc.ExportAll(t.Context(), user)
		require.NoError(t, err)
		require.Equal(t, 3, exp.Total)
		require.False(t, exp.Truncated)
		require.Len(t, exp.Docs, 3)
	})
	t.Run("truncated beyond the export limit", func(t *testing.T) {
		repo := docsbottest.NewFakeRepo()
		const n = 51 // exportLimit (50) + 1
		for i := 0; i < n; i++ {
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
		}
		svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

		exp, err := svc.ExportAll(t.Context(), user)
		require.NoError(t, err)
		require.Equal(t, n, exp.Total)
		require.True(t, exp.Truncated)
		require.Len(t, exp.Docs, 50)
	})
}
