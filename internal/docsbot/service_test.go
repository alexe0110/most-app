package docsbot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

const user docsbot.UserID = "user-1"

func incoming(unique string) docsbot.IncomingFile {
	return docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "f-" + unique,
		FileUniqueID: unique, Title: "Паспорт",
	}
}

func TestService_Receive(t *testing.T) {
	saved := docsbot.Document{
		ID: "d-saved", UserID: user, Status: docsbot.StatusSaved,
		Category: docsbot.CatPassport, Title: "Паспорт", TgFileUniqueID: "dup-saved",
	}
	pending := docsbot.Document{
		ID: "d-pending", UserID: user, Status: docsbot.StatusPending,
		Title: "Паспорт", TgFileUniqueID: "dup-pending",
	}

	tests := []struct {
		name   string
		seed   *docsbot.Document
		unique string
		assert func(t *testing.T, rep docsbot.Reply, repo *fakeRepo)
	}{
		{
			name:   "new file is created and needs a category",
			unique: "fresh",
			assert: func(t *testing.T, rep docsbot.Reply, repo *fakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.StatusPending, nc.Doc.Status)
				require.Len(t, repo.docs, 1)
			},
		},
		{
			name:   "pending duplicate re-asks the category",
			seed:   &pending,
			unique: "dup-pending",
			assert: func(t *testing.T, rep docsbot.Reply, repo *fakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-pending"), nc.Doc.ID)
				require.Len(t, repo.docs, 1, "no new row")
			},
		},
		{
			name:   "saved duplicate reports already saved",
			seed:   &saved,
			unique: "dup-saved",
			assert: func(t *testing.T, rep docsbot.Reply, _ *fakeRepo) {
				as, ok := rep.(docsbot.AlreadySaved)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-saved"), as.Doc.ID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			if tt.seed != nil {
				repo.seed(*tt.seed)
			}
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))
			rep, err := svc.Receive(t.Context(), incoming(tt.unique))
			require.NoError(t, err)
			tt.assert(t, rep, repo)
		})
	}
}

func TestService_ChooseCategory(t *testing.T) {
	tests := []struct {
		name          string
		savedBefore   int // extra saved docs already present
		connected     bool
		cat           docsbot.Category
		wantErr       error
		wantOffer     bool
		wantBackupPed bool
	}{
		{name: "first document offers Drive", cat: docsbot.CatPassport, wantOffer: true},
		{name: "second document does not offer", savedBefore: 1, cat: docsbot.CatPassport},
		{name: "fifth document offers Drive", savedBefore: 4, cat: docsbot.CatAuto, wantOffer: true},
		{name: "connected queues backup, never offers", connected: true, cat: docsbot.CatPassport, wantBackupPed: true},
		{name: "unknown category rejected", cat: docsbot.Category("bogus"), wantErr: docsbot.ErrUnknownCategory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for i := 0; i < tt.savedBefore; i++ {
				repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
			uploads := &fakeUploads{repo: repo}
			svc := docsbot.NewService(repo, fakeDrive{connected: tt.connected}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, tt.cat)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOffer, got.OfferDrive)
			require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
			require.Equal(t, tt.cat, repo.docs[doc.ID].Category)
			if tt.wantBackupPed {
				require.Equal(t, []docsbot.DocumentID{doc.ID}, uploads.calls, "upload enqueued once")
				require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
			} else {
				require.Empty(t, uploads.calls)
				require.Equal(t, docsbot.BackupNone, repo.docs[doc.ID].BackupStatus)
			}
		})
	}
}

func TestService_ChooseCategory_defaultTitle(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const base = "Медицина — 08.11.2026"
	taken := func(titles ...string) []docsbot.Document {
		var out []docsbot.Document
		for _, title := range titles {
			out = append(out, docsbot.Document{
				UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatMedical, Title: title,
			})
		}
		return out
	}
	tests := []struct {
		name      string
		title     string
		existing  []docsbot.Document
		wantTitle string
	}{
		{name: "blank title gets default", title: "", wantTitle: base},
		{name: "whitespace title gets default", title: "   ", wantTitle: base},
		{name: "first collision adds (2)", existing: taken(base), wantTitle: base + " (2)"},
		{name: "second collision adds (3)", existing: taken(base, base+" (2)"), wantTitle: base + " (3)"},
		{name: "same title in another category is no collision", existing: []docsbot.Document{{
			UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: base,
		}}, wantTitle: base},
		{name: "existing title is kept", title: "Анализы.pdf", existing: taken(base), wantTitle: "Анализы.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for _, d := range tt.existing {
				repo.seed(d)
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: tt.title, TgFileUniqueID: "u"})
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo),
				docsbot.WithClock(func() time.Time { return now }),
				docsbot.WithCategoryNamer(func(docsbot.Category) string { return "Медицина" }),
			)

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatMedical)
			require.NoError(t, err)
			require.Equal(t, tt.wantTitle, got.Doc.Title)
			require.Equal(t, tt.wantTitle, repo.docs[doc.ID].Title)
		})
	}
}

func TestService_ChooseCategory_idempotent(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, TgFileUniqueID: "u",
	})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatReceipts)
	require.NoError(t, err)
	require.False(t, got.OfferDrive)
	require.Equal(t, docsbot.CatPassport, repo.docs[doc.ID].Category, "a second tap must not change the category")
}

func TestService_Rename(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	_, err := svc.Rename(t.Context(), user, doc.ID, "   ")
	require.ErrorIs(t, err, docsbot.ErrEmptyTitle)

	card, err := svc.Rename(t.Context(), user, doc.ID, "  Новый паспорт  ")
	require.NoError(t, err)
	require.Equal(t, "Новый паспорт", card.Doc.Title)
}

func TestService_Recategorize_rejectsUnknown(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	_, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.Category("nope"))
	require.ErrorIs(t, err, docsbot.ErrUnknownCategory)
}

func TestService_CategoryPage_paginates(t *testing.T) {
	repo := newFakeRepo()
	for i := 0; i < 25; i++ {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 2)
	require.NoError(t, err)
	require.Equal(t, 25, page.Total)
	require.Equal(t, 3, page.Pages)
	require.Equal(t, 2, page.Page)
	require.Len(t, page.Docs, 10)

	// An out-of-range page is clamped to the last one.
	last, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 99)
	require.NoError(t, err)
	require.Equal(t, 3, last.Page)
	require.Len(t, last.Docs, 5)
}

func TestService_Search(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Паспорт РК"})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Договор аренды"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	found, err := svc.Search(t.Context(), user, "паспорт")
	require.NoError(t, err)
	require.Len(t, found.Docs, 1)
	require.Equal(t, "Паспорт РК", found.Docs[0].Title)
}

func TestService_Backup_missingDocumentIsNoop(t *testing.T) {
	repo := newFakeRepo()
	var uploads int
	svc := docsbot.NewService(repo, fakeDrive{uploads: &uploads}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

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
			svc := docsbot.NewService(repo, tt.drive, tt.files, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

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
	svc := docsbot.NewService(repo, drive, fakeFiles{data: []byte("x")}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	res, err := svc.Backup(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.True(t, res.Noop)
	require.Equal(t, docsbot.BackupNone, repo.docs[doc.ID].BackupStatus)
}

func TestService_TransferSaved(t *testing.T) {
	repo := newFakeRepo()
	a := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupBackedUp})
	b := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending})
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

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
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

	_, ok, err := svc.TransferSaved(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, uploads.requeued)
}

func TestService_MarkBackupFailed(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupUploading})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	require.NoError(t, svc.MarkBackupFailed(t.Context(), user, doc.ID))
	require.Equal(t, docsbot.BackupFailed, repo.docs[doc.ID].BackupStatus)
}

func TestService_Settings(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	for i := 0; i < 3; i++ {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	}
	svc := docsbot.NewService(repo, fakeDrive{connected: true}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	view, err := svc.Settings(t.Context(), user)
	require.NoError(t, err)
	require.Equal(t, docsbot.SettingsView{
		DriveConnected: true, Total: 6, InDrive: 2, OnlyTelegram: 3, Failed: 1,
	}, view)
}

func TestService_ResetDriveFolders(t *testing.T) {
	repo := newFakeRepo()
	repo.folders = map[docsbot.Category]string{"passport": "f1", "insurance": "f2"}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

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
		svc := docsbot.NewService(repo, fakeDrive{disconnected: &disconnected, revoked: &revoked}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

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
		svc := docsbot.NewService(repo, fakeDrive{disconnectErr: errors.New("boom")}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

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
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

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
	svc := docsbot.NewService(repo, fakeDrive{revoked: &revoked}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

	require.NoError(t, svc.DeleteAllData(t.Context(), user))
	require.True(t, revoked)
	require.Equal(t, []docsbot.UserID{user}, uploads.allJobs, "every pending job goes with the data")
	require.Empty(t, uploads.cancels)
	require.Equal(t, []docsbot.UserID{user}, repo.deleted)
	require.Empty(t, repo.docs)
}

func TestService_RetryFailed(t *testing.T) {
	repo := newFakeRepo()
	a := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupBackedUp})
	b := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupNone})
	uploads := &fakeUploads{repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo))

	view, ok, err := svc.RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, view.Lines, 2)
	require.Equal(t, docsbot.StatusTransfer, view.Message.Kind)
	require.Equal(t, view.Message.ID, repo.docs[a.ID].StatusMessageID)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, uploads.requeued)

	empty := newFakeRepo()
	_, ok, err = docsbot.NewService(empty, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: empty}, empty, newFakeStatus(empty)).
		RetryFailed(t.Context(), user, 5, 50)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestService_ExportAll(t *testing.T) {
	t.Run("not truncated", func(t *testing.T) {
		repo := newFakeRepo()
		for i := 0; i < 3; i++ {
			repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
		}
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

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
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

		exp, err := svc.ExportAll(t.Context(), user)
		require.NoError(t, err)
		require.Equal(t, n, exp.Total)
		require.True(t, exp.Truncated)
		require.Len(t, exp.Docs, 50)
	})
}

func TestService_folderLinks(t *testing.T) {
	tests := []struct {
		name    string
		folders map[docsbot.Category]string
		want    string
	}{
		{"known folder", map[docsbot.Category]string{docsbot.CatReceipts: "folder-1"}, "folder-1"},
		{"no folder yet", nil, ""},
		{"other category only", map[docsbot.Category]string{docsbot.CatAuto: "folder-2"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.folders = tt.folders
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

			card, err := svc.Card(t.Context(), user, doc.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, card.CategoryFolderID)

			page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
			require.NoError(t, err)
			require.Equal(t, tt.want, page.FolderID)
		})
	}
}

type failingFolders struct{ err error }

func (f failingFolders) CategoryFolderID(context.Context, docsbot.UserID, docsbot.Category) (string, bool, error) {
	return "", false, f.err
}

func TestService_folderLinks_error(t *testing.T) {
	boom := errors.New("boom")
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, failingFolders{err: boom}, newFakeStatus(repo))

	_, err := svc.Card(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, boom)

	_, err = svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
	require.ErrorIs(t, err, boom)
}

func TestService_Card_pendingNoFolder(t *testing.T) {
	repo := newFakeRepo()
	repo.folders = map[docsbot.Category]string{docsbot.CatReceipts: "folder-1"}
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "x", TgFileUniqueID: "u"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

	card, err := svc.Card(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.Empty(t, card.CategoryFolderID)
}

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
			repo := newFakeRepo()
			status := newFakeStatus(repo)
			if tt.msg != nil {
				status.msgs[tt.msg.ID] = *tt.msg
			}
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, status,
				docsbot.WithClock(func() time.Time { return now }))

			sm, ok, err := svc.StatusTarget(context.Background(), user)
			require.NoError(t, err)
			require.Equal(t, tt.reuse, ok)
			require.Equal(t, tt.wantID, sm.ID)
		})
	}
}

func TestService_StatusView(t *testing.T) {
	t.Run("not grouped", func(t *testing.T) {
		repo := newFakeRepo()
		doc := repo.seed(docsbot.Document{UserID: user})
		svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo))

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
			repo := newFakeRepo()
			repo.folders = map[docsbot.Category]string{docsbot.CatPassport: "folder-p"}
			status := newFakeStatus(repo)
			sm, err := status.Create(context.Background(), user, 7, 70, docsbot.StatusBatch)
			require.NoError(t, err)
			a := repo.seed(docsbot.Document{UserID: user, Category: docsbot.CatPassport, BackupStatus: docsbot.BackupBackedUp, StatusMessageID: sm.ID})
			b := repo.seed(docsbot.Document{UserID: user, Category: docsbot.CatAuto, BackupStatus: tt.second, StatusMessageID: sm.ID})
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, status)

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
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, uploads, repo, newFakeStatus(repo),
				docsbot.WithClock(func() time.Time { return now }))

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
				require.Equal(t, doc.ID, res.Failed[0].ID)
				require.Equal(t, user, res.Failed[0].User)
			}
		})
	}
}
