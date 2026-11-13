package docsbot_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_DeleteAllData_revokeFailureDoesNotBlockDeletion(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	boom := errors.New("google unreachable")
	svc, uploads := edgeService(repo, fakeDrive{revokeErr: boom})

	err := svc.DeleteAllData(t.Context(), user)

	require.ErrorIs(t, err, docsbot.ErrRevokeIncomplete)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []docsbot.UserID{user}, uploads.allJobs)
	require.Equal(t, []docsbot.UserID{user}, repo.deleted, "data must be deleted despite the revoke failure")
	require.Empty(t, repo.docs)
}

func TestService_Settings(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	for i := 0; i < 3; i++ {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	view, err := svc.Settings(t.Context(), user)
	require.NoError(t, err)
	require.Equal(t, docsbot.SettingsView{
		DriveConnected: true, Total: 6, InDrive: 2, OnlyTelegram: 3, Failed: 1, InlineEnabled: true,
	}, view)
}

func TestService_ResetDriveFolders(t *testing.T) {
	repo := newFakeRepo()
	repo.folders = map[docsbot.Category]string{"passport": "f1", "insurance": "f2"}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	require.NoError(t, svc.ResetDriveFolders(t.Context(), user))
	require.Empty(t, repo.folders)
}

func TestService_Disconnect(t *testing.T) {
	t.Run("disconnects, cancels uploads and resets stuck documents", func(t *testing.T) {
		repo := newFakeRepo()
		var disconnected, revoked bool
		pending := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploading := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
		failed := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
		other := repo.seed(docsbot.Document{UserID: "other", Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploads := &fakeUploads{repo: repo}
		svc := docsbot.NewService(repo, fakeDrive{disconnected: &disconnected, revoked: &revoked}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		require.NoError(t, svc.Disconnect(t.Context(), user))
		require.True(t, disconnected)
		require.False(t, revoked, "user disconnect must not use Revoke")
		require.Equal(t, []docsbot.UserID{user}, uploads.cancels)
		require.Empty(t, uploads.allJobs, "sync and trash jobs stay on disconnect")
		require.Equal(t, docsbot.BackupNone, repo.docs[pending.ID].BackupStatus)
		require.Equal(t, docsbot.BackupNone, repo.docs[uploading.ID].BackupStatus)
		require.Equal(t, docsbot.BackupFailed, repo.docs[failed.ID].BackupStatus)
		require.Equal(t, docsbot.BackupPending, repo.docs[other.ID].BackupStatus, "other users untouched")
	})
	t.Run("drive failure is a hard error and cancels nothing", func(t *testing.T) {
		repo := newFakeRepo()
		pending := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupPending})
		uploads := &fakeUploads{repo: repo}
		svc := docsbot.NewService(repo, fakeDrive{disconnectErr: errors.New("boom")}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		err := svc.Disconnect(t.Context(), user)
		require.Error(t, err)
		require.NotErrorIs(t, err, docsbot.ErrRevokeIncomplete)
		require.Empty(t, uploads.cancels, "the integration is still active")
		require.Equal(t, docsbot.BackupPending, repo.docs[pending.ID].BackupStatus)
	})
	// Criterion 2: disconnect keeps stored documents — it must not delete them,
	// clear a backed-up doc's drive_file_id or change its backup_status.
	t.Run("leaves stored documents untouched", func(t *testing.T) {
		repo := newFakeRepo()
		backed := repo.seed(docsbot.Document{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport,
			BackupStatus: docsbot.BackupBackedUp, DriveFileID: "drv-1",
		})
		onlyTg := repo.seed(docsbot.Document{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
			BackupStatus: docsbot.BackupNone,
		})
		uploads := &fakeUploads{repo: repo}
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		require.NoError(t, svc.Disconnect(t.Context(), user))

		require.Empty(t, repo.deleted, "disconnect must not delete user data")
		require.Len(t, repo.docs, 2, "no documents removed")
		require.Equal(t, docsbot.BackupBackedUp, repo.docs[backed.ID].BackupStatus, "backed-up status kept")
		require.Equal(t, "drv-1", repo.docs[backed.ID].DriveFileID, "drive_file_id kept")
		require.Equal(t, docsbot.BackupNone, repo.docs[onlyTg.ID].BackupStatus, "telegram-only doc unchanged")
	})
}

func TestService_DeleteAllData(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	var revoked bool
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{revoked: &revoked}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	require.NoError(t, svc.DeleteAllData(t.Context(), user))
	require.True(t, revoked)
	require.Equal(t, []docsbot.UserID{user}, uploads.allJobs, "every pending job goes with the data")
	require.Empty(t, uploads.cancels)
	require.Equal(t, []docsbot.UserID{user}, repo.deleted)
	require.Empty(t, repo.docs)
}

func TestService_ExportAll(t *testing.T) {
	t.Run("not truncated", func(t *testing.T) {
		repo := newFakeRepo()
		for i := 0; i < 3; i++ {
			repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
		}
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		exp, err := svc.ExportAll(t.Context(), user)
		require.NoError(t, err)
		require.Equal(t, 3, exp.Total)
		require.False(t, exp.Truncated)
		require.Len(t, exp.Docs, 3)
	})
	t.Run("truncated beyond the export limit", func(t *testing.T) {
		repo := newFakeRepo()
		const n = 51 // exportLimit (50) + 1
		for i := 0; i < n; i++ {
			repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
		}
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		exp, err := svc.ExportAll(t.Context(), user)
		require.NoError(t, err)
		require.Equal(t, n, exp.Total)
		require.True(t, exp.Truncated)
		require.Len(t, exp.Docs, 50)
	})
}
