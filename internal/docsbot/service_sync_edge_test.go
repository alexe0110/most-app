package docsbot_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_RenameRecategorize_notQueuedWhileUploading(t *testing.T) {
	for _, st := range []docsbot.BackupStatus{docsbot.BackupPending, docsbot.BackupUploading, docsbot.BackupFailed, docsbot.BackupNone} {
		t.Run(string(st), func(t *testing.T) {
			repo := newFakeRepo()
			doc := backedUpDoc(repo, func(d *docsbot.Document) { d.BackupStatus, d.DriveFileID = st, "" })
			svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

			_, err := svc.Rename(t.Context(), user, doc.ID, "Новое")
			require.NoError(t, err)
			_, err = svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatPassport)
			require.NoError(t, err)

			got := repo.docs[doc.ID]
			require.Equal(t, "Новое", got.Title)
			require.Equal(t, docsbot.CatPassport, got.Category)
			require.Equal(t, 2, got.MetaVersion, "meta_version moves so the upload can notice")
			require.Empty(t, syncs.jobs)
		})
	}
}

func TestService_Recategorize_disconnectedMissingStaysMissing(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing })
	svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

	card, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatPassport)

	require.NoError(t, err)
	require.Equal(t, docsbot.SyncMissing, card.Doc.SyncStatus)
	require.Empty(t, syncs.jobs)
}

func TestService_Delete_errors(t *testing.T) {
	t.Run("drive status error keeps the row", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc, syncs := syncService(repo, fakeDrive{err: errors.New("db down")}, fakeFiles{})

		_, err := svc.Delete(t.Context(), user, doc.ID)

		require.Error(t, err)
		require.Contains(t, repo.docs, doc.ID)
		require.Empty(t, syncs.trashes)
	})
	t.Run("trash enqueue failure keeps the row", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})
		syncs.err = errors.New("queue down")

		_, err := svc.Delete(t.Context(), user, doc.ID)

		require.Error(t, err)
		require.Contains(t, repo.docs, doc.ID)
	})
	t.Run("foreign and unknown documents", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

		_, err := svc.Delete(t.Context(), stranger, doc.ID)
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		_, err = svc.Delete(t.Context(), user, "nope")
		require.ErrorIs(t, err, docsbot.ErrNotFound)
		require.Contains(t, repo.docs, doc.ID)
		require.Empty(t, syncs.trashes)
	})
	t.Run("missing remote file queues nothing and keeps nothing", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing })
		svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

		got, err := svc.Delete(t.Context(), user, doc.ID)

		require.NoError(t, err)
		require.Equal(t, docsbot.Deleted{}, got)
		require.Empty(t, syncs.trashes)
	})
}

func TestService_SyncRemote_dirtyContentErrors(t *testing.T) {
	tests := []struct {
		name        string
		drive       func(calls *[]string) fakeDrive
		files       fakeFiles
		wantErr     error
		wantMissing bool
		wantSync    docsbot.SyncStatus
		wantCalls   []string
	}{
		{
			name:        "replace 404 marks missing",
			drive:       func(c *[]string) fakeDrive { return fakeDrive{calls: c, replaceErr: docsbot.ErrRemoteMissing} },
			files:       fakeFiles{data: []byte("x"), mime: "application/pdf"},
			wantMissing: true, wantSync: docsbot.SyncMissing, wantCalls: []string{"replace:remote-1"},
		},
		{
			name:     "replace revoked is terminal for the worker",
			drive:    func(c *[]string) fakeDrive { return fakeDrive{calls: c, replaceErr: docsbot.ErrDriveRevoked} },
			files:    fakeFiles{data: []byte("x"), mime: "application/pdf"},
			wantErr:  docsbot.ErrDriveRevoked,
			wantSync: docsbot.SyncPending, wantCalls: []string{"replace:remote-1"},
		},
		{
			name:     "replace transient error is retried and nothing else runs",
			drive:    func(c *[]string) fakeDrive { return fakeDrive{calls: c, replaceErr: errors.New("429")} },
			files:    fakeFiles{data: []byte("x"), mime: "application/pdf"},
			wantErr:  errors.New("429"),
			wantSync: docsbot.SyncPending, wantCalls: []string{"replace:remote-1"},
		},
		{
			name:     "download error does not touch drive",
			drive:    func(c *[]string) fakeDrive { return fakeDrive{calls: c} },
			files:    fakeFiles{err: errors.New("tg down")},
			wantErr:  errors.New("tg down"),
			wantSync: docsbot.SyncPending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus, d.ContentDirty = docsbot.SyncPending, true })
			var calls []string
			svc, _ := syncService(repo, tt.drive(&calls), tt.files)

			res, err := svc.SyncRemote(t.Context(), user, doc.ID)

			switch {
			case tt.wantErr == nil:
				require.NoError(t, err)
			case errors.Is(tt.wantErr, docsbot.ErrDriveRevoked):
				require.ErrorIs(t, err, docsbot.ErrDriveRevoked)
			default:
				require.ErrorContains(t, err, tt.wantErr.Error())
			}
			require.Equal(t, tt.wantMissing, res.Missing)
			require.Equal(t, tt.wantSync, repo.docs[doc.ID].SyncStatus)
			require.True(t, repo.docs[doc.ID].ContentDirty, "content stays dirty until a sync succeeds")
			require.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestService_SyncRemote_cancelledContextKeepsPending(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending })
	svc, _ := syncService(repo, fakeDrive{moveErr: context.Canceled}, fakeFiles{})

	_, err := svc.SyncRemote(t.Context(), user, doc.ID)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, docsbot.SyncPending, repo.docs[doc.ID].SyncStatus)
}

func TestService_RetrySync_notFailedStates(t *testing.T) {
	for _, st := range []docsbot.SyncStatus{docsbot.SyncOK, docsbot.SyncPending, docsbot.SyncMissing} {
		t.Run(string(st), func(t *testing.T) {
			repo := newFakeRepo()
			doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = st })
			svc, syncs := syncService(repo, fakeDrive{}, fakeFiles{})

			require.ErrorIs(t, svc.RetrySync(t.Context(), user, doc.ID), docsbot.ErrNotRetryable)
			require.Equal(t, st, repo.docs[doc.ID].SyncStatus)
			require.Empty(t, syncs.jobs)
		})
	}
}

// --- races (run with -race) ---

func parallel(n int, fn func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			fn(i)
		}()
	}
	close(start)
	wg.Wait()
}

func TestService_race_doubleTapRenameAndCategory(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

	const n = 8
	parallel(n, func(i int) {
		if i%2 == 0 {
			_, err := svc.Rename(t.Context(), user, doc.ID, "Одно имя")
			require.NoError(t, err)
			return
		}
		_, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatPassport)
		require.NoError(t, err)
	})

	got := repo.docs[doc.ID]
	require.Equal(t, "Одно имя", got.Title)
	require.Equal(t, docsbot.CatPassport, got.Category)
	require.Equal(t, n, got.MetaVersion)
	require.Equal(t, docsbot.SyncPending, got.SyncStatus)
	require.NotEmpty(t, syncs.jobs)
}

func TestService_race_doubleTapDeleteTrashesOnce(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})

	var ok, gone atomic.Int32
	parallel(6, func(int) {
		_, err := svc.Delete(t.Context(), user, doc.ID)
		switch {
		case err == nil:
			ok.Add(1)
		case errors.Is(err, docsbot.ErrNotFound):
			gone.Add(1)
		default:
			t.Errorf("unexpected error: %v", err)
		}
	})

	require.EqualValues(t, 1, ok.Load())
	require.EqualValues(t, 5, gone.Load())
	require.Equal(t, []string{"remote-1"}, syncs.trashes, "one trash job, not one per tap")
	require.Empty(t, repo.docs)
}

func TestService_race_doubleTapReplaceQueuesOneSync(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.TgFileID, d.TgFileUniqueID = "tg-cur", "uniq-cur" })
	svc, syncs := syncService(repo, fakeDrive{connected: true}, fakeFiles{})
	in := docsbot.IncomingFile{FileID: "tg-new", FileUniqueID: "uniq-new", MIME: "application/pdf"}

	parallel(6, func(int) {
		rep, err := svc.ReplaceContent(t.Context(), user, doc.ID, in)
		require.NoError(t, err)
		switch rep.(type) {
		case docsbot.Replaced, docsbot.ReplaceBusy:
		default:
			t.Errorf("unexpected reply %T", rep)
		}
	})

	got := repo.docs[doc.ID]
	require.Equal(t, "tg-new", got.TgFileID)
	require.Equal(t, 1, got.MetaVersion, "swapped exactly once")
	require.Len(t, syncs.jobs, 1)
}

func TestService_race_doubleTapRetrySync(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncFailed })
	svc, syncs := syncService(repo, fakeDrive{}, fakeFiles{})

	var ok atomic.Int32
	parallel(6, func(int) {
		err := svc.RetrySync(t.Context(), user, doc.ID)
		if err == nil {
			ok.Add(1)
		} else if !errors.Is(err, docsbot.ErrNotRetryable) {
			t.Errorf("unexpected error: %v", err)
		}
	})

	require.EqualValues(t, 1, ok.Load())
	require.Len(t, syncs.jobs, 1)
}

// deletedDuringSync removes the document while the worker talks to Drive.
type deletedDuringSync struct {
	fakeDrive
	syncs *fakeSyncs
	id    docsbot.DocumentID
	svc   func() *docsbot.Service
}

func (d deletedDuringSync) Rename(ctx context.Context, user docsbot.UserID, remoteID, name string) error {
	if _, err := d.svc().Delete(ctx, user, d.id); err != nil {
		return err
	}
	return d.fakeDrive.Rename(ctx, user, remoteID, name)
}

func TestService_race_deleteDuringSync(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending })
	syncs := &fakeSyncs{repo: repo}
	var svc *docsbot.Service
	drive := deletedDuringSync{fakeDrive: fakeDrive{connected: true}, syncs: syncs, id: doc.ID, svc: func() *docsbot.Service { return svc }}
	svc = docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), syncs)

	res, err := svc.SyncRemote(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.True(t, res.Noop, "the sync finishes silently once the row is gone")
	require.Equal(t, []string{"remote-1"}, syncs.trashes)
	require.Empty(t, syncs.jobs, "no follow-up sync for a deleted document")
}

// renamedDuringUpload renames the document while its file uploads.
type renamedDuringUpload struct {
	fakeDrive
	repo  *fakeRepo
	id    docsbot.DocumentID
	title string
}

func (d renamedDuringUpload) Upload(ctx context.Context, user docsbot.UserID, in docsbot.DriveUpload) (docsbot.DriveFile, error) {
	if d.title != "" {
		_ = d.repo.SetTitle(ctx, user, d.id, d.title)
	}
	return d.fakeDrive.Upload(ctx, user, in)
}

func TestService_Backup_syncQueuedExactlyWhenVersionChanged(t *testing.T) {
	tests := []struct {
		name     string
		rename   string
		wantJobs int
	}{
		{"no change while uploading", "", 0},
		{"renamed while uploading", "Новое", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{
				UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "Старое", Kind: docsbot.KindDocument,
				BackupStatus: docsbot.BackupPending, TgFileID: "tg-1", MIME: "application/pdf",
			})
			syncs := &fakeSyncs{repo: repo}
			drive := renamedDuringUpload{
				fakeDrive: fakeDrive{connected: true, uploadFile: docsbot.DriveFile{ID: "remote-1"}},
				repo:      repo, id: doc.ID, title: tt.rename,
			}
			svc := docsbot.NewService(repo, drive, fakeFiles{data: []byte("x"), mime: "application/pdf"}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), syncs)

			res, err := svc.Backup(t.Context(), user, doc.ID)

			require.NoError(t, err)
			require.Equal(t, docsbot.BackupBackedUp, repo.docs[doc.ID].BackupStatus)
			require.Len(t, syncs.jobs, tt.wantJobs)
			if tt.wantJobs > 0 {
				require.Equal(t, docsbot.SyncPending, res.Doc.SyncStatus)
			}
		})
	}
}
