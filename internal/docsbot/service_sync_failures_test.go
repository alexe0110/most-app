package docsbot_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
