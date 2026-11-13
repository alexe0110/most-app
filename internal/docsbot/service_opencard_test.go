package docsbot_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// trueDrive reports the file as present and runs onCheck during the lookup.
type trueDrive struct {
	fakeDrive
	onCheck func()
}

func (d *trueDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	d.onCheck()
	return true, nil
}

func TestService_OpenCard_missingToOkDoesNotOverwritePendingSync(t *testing.T) {
	// A rename queued a sync while the existence check was in flight: the
	// conditional missing -> ok update must lose to the newer pending state.
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing })
	drive := &trueDrive{fakeDrive: fakeDrive{connected: true}, onCheck: func() {
		_ = repo.SetSyncStatus(context.Background(), user, doc.ID, docsbot.SyncPending, "")
	}}
	svc := docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	card, err := svc.OpenCard(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.Equal(t, docsbot.SyncMissing, card.Doc.SyncStatus, "card shows what was read")
	require.Equal(t, docsbot.SyncPending, repo.docs[doc.ID].SyncStatus, "row keeps the newer state")
}

func TestService_OpenCard_cancelledContextLeavesCardUnchanged(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	ctx, cancel := context.WithCancel(t.Context())
	drive := &racingDrive{fakeDrive: fakeDrive{connected: true}, onCheck: cancel} // reports "deleted" after cancel
	svc := docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	// The drive says "gone" but the caller is already cancelled: whatever the
	// service decides, it must not panic and must return the card.
	card, err := svc.OpenCard(ctx, user, doc.ID)

	require.NoError(t, err)
	require.Equal(t, doc.ID, card.Doc.ID)
}

func TestService_OpenCard(t *testing.T) {
	tests := []struct {
		name       string
		connected  bool
		mod        func(*docsbot.Document)
		exists     bool
		existsErr  error
		wantSync   docsbot.SyncStatus
		wantChecks int
	}{
		{"file there: unchanged", true, nil, true, nil, docsbot.SyncOK, 1},
		{"file deleted: ok -> missing", true, nil, false, nil, docsbot.SyncMissing, 1},
		{"file back: missing -> ok", true, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing }, true, nil, docsbot.SyncOK, 1},
		{"still missing", true, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing }, false, nil, docsbot.SyncMissing, 1},
		{"network error: unchanged", true, nil, false, errors.New("boom"), docsbot.SyncOK, 1},
		{"timeout: unchanged", true, nil, false, context.DeadlineExceeded, docsbot.SyncOK, 1},
		{"revoked: unchanged", true, nil, false, docsbot.ErrDriveRevoked, docsbot.SyncOK, 1},
		{"disconnected on check: unchanged", true, nil, false, docsbot.ErrDriveDisconnected, docsbot.SyncOK, 1},
		{"drive not connected: no check", false, nil, false, nil, docsbot.SyncOK, 0},
		{"sync pending: no check", true, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending }, false, nil, docsbot.SyncPending, 0},
		{"sync failed: no check", true, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncFailed }, false, nil, docsbot.SyncFailed, 0},
		{"not in drive: no check", true, func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupNone }, false, nil, docsbot.SyncOK, 0},
		{"no remote id: no check", true, func(d *docsbot.Document) { d.DriveFileID = "" }, false, nil, docsbot.SyncOK, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := backedUpDoc(repo, tt.mod)
			var calls int
			var left time.Duration
			drive := fakeDrive{connected: tt.connected, existsRes: tt.exists, existsErr: tt.existsErr, existsCalls: &calls, checkDeadline: &left}
			svc, syncs := syncService(repo, drive, fakeFiles{})

			card, err := svc.OpenCard(t.Context(), user, doc.ID)

			require.NoError(t, err)
			require.Equal(t, tt.wantChecks, calls)
			require.Equal(t, tt.wantSync, card.Doc.SyncStatus, "card")
			require.Equal(t, tt.wantSync, repo.docs[doc.ID].SyncStatus, "row")
			require.Empty(t, syncs.jobs, "no re-upload or sync is queued")
			if tt.wantChecks > 0 {
				require.Positive(t, left)
				require.LessOrEqual(t, left, 3*time.Second, "the check is bounded to 3s")
			}
		})
	}
}

func TestService_OpenCard_throttle(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	other := backedUpDoc(repo, func(d *docsbot.Document) { d.TgFileUniqueID = "other" })
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	var calls int
	svc := docsbot.NewService(repo, fakeDrive{connected: true, existsRes: true, existsCalls: &calls}, fakeFiles{},
		&fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo},
		docsbot.WithClock(func() time.Time { return now }))
	open := func(id docsbot.DocumentID) {
		t.Helper()
		_, err := svc.OpenCard(t.Context(), user, id)
		require.NoError(t, err)
	}

	open(doc.ID)
	require.Equal(t, 1, calls)
	open(doc.ID)
	require.Equal(t, 1, calls, "second open within a minute is not checked")
	open(other.ID)
	require.Equal(t, 2, calls, "the throttle is per document")

	now = now.Add(59 * time.Second)
	open(doc.ID)
	require.Equal(t, 2, calls)
	now = now.Add(time.Second)
	open(doc.ID)
	require.Equal(t, 3, calls, "a minute later it is checked again")
}

func TestService_OpenCard_failedCheckIsThrottledToo(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	var calls int
	svc, _ := syncService(repo, fakeDrive{connected: true, existsErr: errors.New("net"), existsCalls: &calls}, fakeFiles{})
	for range 3 {
		_, err := svc.OpenCard(t.Context(), user, doc.ID)
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls, "an outage must not turn every tap into a Drive call")
}

func TestService_OpenCard_notFound(t *testing.T) {
	svc, _ := syncService(newFakeRepo(), fakeDrive{connected: true}, fakeFiles{})
	_, err := svc.OpenCard(t.Context(), user, "nope")
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestService_OpenCard_doesNotOverwriteNewerSync(t *testing.T) {
	// The row moved to pending between the read and the transition (an edit queued
	// a sync): the conditional update must not clobber it.
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	drive := &racingDrive{fakeDrive: fakeDrive{connected: true}, onCheck: func() {
		_ = repo.SetSyncStatus(context.Background(), user, doc.ID, docsbot.SyncPending, "")
	}}
	svc := docsbot.NewService(repo, drive, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	card, err := svc.OpenCard(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.Equal(t, docsbot.SyncOK, card.Doc.SyncStatus, "card shows what was read")
	require.Equal(t, docsbot.SyncPending, repo.docs[doc.ID].SyncStatus, "row keeps the newer state")
}

// racingDrive runs onCheck during FileExists and reports the file as deleted.
type racingDrive struct {
	fakeDrive
	onCheck func()
}

func (d *racingDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	d.onCheck()
	return false, nil
}

// failingTransitionRepo fails the conditional sync-status update.
type failingTransitionRepo struct{ *fakeRepo }

func (failingTransitionRepo) TransitionSyncStatus(context.Context, docsbot.UserID, docsbot.DocumentID, string, docsbot.SyncStatus, docsbot.SyncStatus) (bool, error) {
	return false, errors.New("db down")
}

func TestService_OpenCard_databaseErrorsComeBackWithTheCard(t *testing.T) {
	t.Run("drive status", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc, _ := syncService(repo, fakeDrive{connected: true, err: errors.New("db down")}, fakeFiles{})

		card, err := svc.OpenCard(t.Context(), user, doc.ID)

		require.Error(t, err)
		require.Equal(t, doc.ID, card.Doc.ID, "the card read so far is returned")
	})
	t.Run("transition", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc := docsbot.NewService(failingTransitionRepo{repo}, fakeDrive{connected: true}, fakeFiles{}, &fakeUploads{repo: repo},
			repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

		card, err := svc.OpenCard(t.Context(), user, doc.ID)

		require.Error(t, err)
		require.Equal(t, doc.ID, card.Doc.ID)
		require.Equal(t, docsbot.SyncOK, card.Doc.SyncStatus)
	})
	t.Run("a Drive check failure is not an error", func(t *testing.T) {
		repo := newFakeRepo()
		doc := backedUpDoc(repo, nil)
		svc, _ := syncService(repo, fakeDrive{connected: true, existsErr: errors.New("net")}, fakeFiles{})
		_, err := svc.OpenCard(t.Context(), user, doc.ID)
		require.NoError(t, err)
	})
}

func TestService_OpenCard_throttleIsCheckedBeforeDriveStatus(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	var calls int
	svc, _ := syncService(repo, fakeDrive{connected: true, existsRes: true, existsCalls: &calls}, fakeFiles{})
	_, err := svc.OpenCard(t.Context(), user, doc.ID)
	require.NoError(t, err)

	// Within the window the status lookup is not even attempted, so its failure is not seen.
	svc2 := docsbot.NewService(repo, fakeDrive{connected: true, err: errors.New("db down")}, fakeFiles{},
		&fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})
	_, err = svc2.OpenCard(t.Context(), user, doc.ID)
	require.Error(t, err, "first open on svc2 does consult the status")
	_, err = svc2.OpenCard(t.Context(), user, doc.ID)
	require.NoError(t, err, "the second one is throttled before Connected")
}
