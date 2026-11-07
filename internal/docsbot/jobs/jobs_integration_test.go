//go:build integration

package jobs_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

type fakeDrive struct{}

func (fakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) { return true, nil }
func (fakeDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{ID: "drive-1", Link: "https://drive/1"}, nil
}

type fakeFiles struct{ err error }

func (f fakeFiles) Download(context.Context, string) ([]byte, string, error) {
	return []byte("data"), "application/pdf", f.err
}

type spyNotifier struct {
	mu       sync.Mutex
	backedUp []docsbot.DocumentID
}

func (s *spyNotifier) BackedUp(_ context.Context, _ docsbot.UserID, d docsbot.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backedUp = append(s.backedUp, d.ID)
	return nil
}
func (*spyNotifier) BackupFailed(context.Context, docsbot.UserID, docsbot.Document) error { return nil }
func (*spyNotifier) Reconnect(context.Context, docsbot.UserID) error                      { return nil }

func (s *spyNotifier) seen() []docsbot.DocumentID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]docsbot.DocumentID(nil), s.backedUp...)
}

// runUpload enqueues one saved document through the real queue and returns its
// repo, id and the spy once the worker is running.
func runUpload(t *testing.T, files docsbot.FileSource) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier) {
	t.Helper()
	ctx := t.Context()
	pool = dbtest.New(t)
	repo := postgres.NewRepo(pool)
	log := slog.New(slog.DiscardHandler)

	var uid string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	var did string
	require.NoError(t, pool.QueryRow(ctx,
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id)
		 values ($1::uuid, 'saved', 'document', 'other', 'title', 'file', 'uniq') returning id::text`, uid).Scan(&did))
	user, id = docsbot.UserID(uid), docsbot.DocumentID(did)

	workers := river.NewWorkers()
	bind := jobs.RegisterUploadDocument(workers, log)
	queue, err := pjobs.New(pool, log, workers)
	require.NoError(t, err)
	enq := jobs.NewEnqueuer(repo, queue)
	svc := docsbot.NewService(repo, fakeDrive{}, files, enq)
	spy = &spyNotifier{}
	bind(svc, spy)

	require.NoError(t, queue.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, queue.Stop(stopCtx))
	})

	require.NoError(t, enq.EnqueueUpload(ctx, user, id))
	return pool, user, id, spy
}

func backupState(t *testing.T, pool *pgxpool.Pool, id docsbot.DocumentID) (status, driveID string) {
	t.Helper()
	var drive *string
	require.NoError(t, pool.QueryRow(t.Context(),
		`select backup_status, drive_file_id from documents where id = $1::uuid`, string(id)).Scan(&status, &drive))
	if drive != nil {
		driveID = *drive
	}
	return status, driveID
}

func TestUploadDocument_backsUp(t *testing.T) {
	pool, _, id, spy := runUpload(t, fakeFiles{})

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	_, driveID := backupState(t, pool, id)
	require.Equal(t, "drive-1", driveID)
	require.Eventually(t, func() bool { return len(spy.seen()) == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, id, spy.seen()[0])
}

func TestUploadDocument_tooLarge(t *testing.T) {
	pool, _, id, spy := runUpload(t, fakeFiles{err: docsbot.ErrFileTooLarge})

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "too_large"
	}, 30*time.Second, 100*time.Millisecond)
	require.Empty(t, spy.seen(), "no success notification for a too-large file")
}
