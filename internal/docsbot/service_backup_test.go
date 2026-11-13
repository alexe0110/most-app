package docsbot_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_MarkBackupFailed_ignoresForeignDocument(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
	svc, _ := edgeService(repo, fakeDrive{})

	require.NoError(t, svc.MarkBackupFailed(t.Context(), stranger, doc.ID, docsbot.BackupReasonQuota))
	require.Equal(t, docsbot.BackupUploading, repo.docs[doc.ID].BackupStatus)
}

func TestService_Backup_missingDocumentIsNoop(t *testing.T) {
	repo := newFakeRepo()
	var uploads int
	svc := docsbot.NewService(repo, fakeDrive{uploads: &uploads}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	res, err := svc.Backup(t.Context(), user, "missing")
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupResult{Noop: true}, res)
	require.Zero(t, uploads)
}

func TestService_Backup(t *testing.T) {
	errBoom := errors.New("boom")
	tests := []struct {
		name         string
		backup       docsbot.BackupStatus
		files        fakeFiles
		drive        fakeDrive
		wantErr      error
		wantTooLarge bool
		wantNoop     bool
		wantStatus   docsbot.BackupStatus
		wantFileID   string
		wantDownload int
		wantUpload   int
	}{
		{
			name:       "success",
			files:      fakeFiles{data: []byte("x"), mime: "application/pdf"},
			drive:      fakeDrive{uploadFile: docsbot.DriveFile{ID: "drv-1"}},
			wantStatus: docsbot.BackupBackedUp, wantFileID: "drv-1", wantDownload: 1, wantUpload: 1,
		},
		{
			name:         "too large stays in telegram",
			files:        fakeFiles{err: docsbot.ErrFileTooLarge},
			wantTooLarge: true, wantStatus: docsbot.BackupTooLarge, wantDownload: 1,
		},
		{
			name:    "revoked grant is terminal",
			files:   fakeFiles{data: []byte("x")},
			drive:   fakeDrive{uploadErr: docsbot.ErrDriveRevoked},
			wantErr: docsbot.ErrDriveRevoked, wantStatus: docsbot.BackupUploading, wantDownload: 1, wantUpload: 1,
		},
		{
			name:    "full drive is terminal",
			files:   fakeFiles{data: []byte("x")},
			drive:   fakeDrive{uploadErr: docsbot.ErrDriveFull},
			wantErr: docsbot.ErrDriveFull, wantStatus: docsbot.BackupUploading, wantDownload: 1, wantUpload: 1,
		},
		{
			name:    "transient upload error is retryable",
			files:   fakeFiles{data: []byte("x")},
			drive:   fakeDrive{uploadErr: errBoom},
			wantErr: errBoom, wantStatus: docsbot.BackupUploading, wantDownload: 1, wantUpload: 1,
		},
		{
			name:    "transient download error is retryable",
			files:   fakeFiles{err: errBoom},
			wantErr: errBoom, wantStatus: docsbot.BackupUploading, wantDownload: 1,
		},
		{
			name:       "already backed up is a no-op",
			backup:     docsbot.BackupBackedUp,
			wantNoop:   true,
			wantStatus: docsbot.BackupBackedUp,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{
				UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport,
				Title: "Паспорт", TgFileID: "tg-1", BackupStatus: tt.backup,
			})
			var downloads, uploads int
			tt.files.downloads, tt.drive.uploads = &downloads, &uploads
			svc := docsbot.NewService(repo, tt.drive, tt.files, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			res, err := svc.Backup(t.Context(), user, doc.ID)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantTooLarge, res.TooLarge)
				require.Equal(t, tt.wantNoop, res.Noop)
				require.Equal(t, tt.wantStatus, res.Doc.BackupStatus)
				require.Equal(t, tt.wantFileID, res.Doc.DriveFileID)
			}
			require.Equal(t, tt.wantStatus, repo.docs[doc.ID].BackupStatus)
			require.Equal(t, tt.wantFileID, repo.docs[doc.ID].DriveFileID)
			require.Equal(t, tt.wantDownload, downloads)
			require.Equal(t, tt.wantUpload, uploads)
		})
	}
}

func TestService_Backup_userDisconnectFinishesSilently(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport,
		TgFileID: "tg-1", BackupStatus: docsbot.BackupPending,
	})
	drive := fakeDrive{uploadErr: docsbot.ErrDriveDisconnected}
	svc := docsbot.NewService(repo, drive, fakeFiles{data: []byte("x")}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	res, err := svc.Backup(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.True(t, res.Noop)
	require.Equal(t, docsbot.BackupNone, repo.docs[doc.ID].BackupStatus)
}

func TestService_MarkBackupFailed(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	require.NoError(t, svc.MarkBackupFailed(t.Context(), user, doc.ID, docsbot.BackupReasonQuota))
	require.Equal(t, docsbot.BackupFailed, repo.docs[doc.ID].BackupStatus)
	require.Equal(t, docsbot.BackupReasonQuota, repo.docs[doc.ID].BackupReason)

	// Any later status change clears the reason.
	require.NoError(t, repo.SetBackupStatus(t.Context(), user, doc.ID, docsbot.BackupPending))
	require.Empty(t, repo.docs[doc.ID].BackupReason)
}

func TestService_SweepStuckUploads(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		age          time.Duration
		backup       docsbot.BackupStatus
		wantFound    int
		wantRequeued int
		wantFailed   bool
		wantStatus   docsbot.BackupStatus
	}{
		{name: "stale uploading is requeued", age: time.Hour, backup: docsbot.BackupUploading, wantFound: 1, wantRequeued: 1, wantStatus: docsbot.BackupUploading},
		{name: "stale pending is requeued", age: 31 * time.Minute, backup: docsbot.BackupPending, wantFound: 1, wantRequeued: 1, wantStatus: docsbot.BackupPending},
		{name: "older than a day is failed", age: 25 * time.Hour, backup: docsbot.BackupUploading, wantFound: 1, wantFailed: true, wantStatus: docsbot.BackupFailed},
		{name: "fresh is skipped", age: 10 * time.Minute, backup: docsbot.BackupUploading, wantStatus: docsbot.BackupUploading},
		{name: "backed up is skipped", age: 25 * time.Hour, backup: docsbot.BackupBackedUp, wantStatus: docsbot.BackupBackedUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: tt.backup})
			// The fake has no updated_at column; tests set it here.
			repo.updated = map[docsbot.DocumentID]time.Time{doc.ID: now.Add(-tt.age)}
			uploads := &fakeUploads{repo: repo}
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo), &fakeSyncs{repo: repo}, docsbot.WithClock(func() time.Time { return now }))

			res, err := svc.SweepStuckUploads(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.wantFound, res.Found)
			require.Equal(t, tt.wantRequeued, res.Requeued)
			require.Equal(t, tt.wantFailed, len(res.Failed) == 1)
			require.Equal(t, tt.wantStatus, repo.docs[doc.ID].BackupStatus)
			if tt.wantRequeued == 1 {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.requeued)
			} else {
				require.Empty(t, uploads.requeued)
			}
			if tt.wantFailed {
				require.Equal(t, docsbot.BackupReasonUnavailable, repo.docs[doc.ID].BackupReason)
				require.Equal(t, doc.ID, res.Failed[0].ID)
				require.Equal(t, user, res.Failed[0].User)
			}
		})
	}
}
