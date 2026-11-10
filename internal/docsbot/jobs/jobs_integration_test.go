//go:build integration

package jobs_test

import (
	"context"
	"log/slog"
	"strings"
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
func (fakeDrive) Rename(context.Context, docsbot.UserID, string, string) error { return nil }

func (fakeDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error { return nil }

func (fakeDrive) Trash(context.Context, docsbot.UserID, string) error { return nil }

func (fakeDrive) Replace(context.Context, docsbot.UserID, string, string, []byte) error { return nil }

func (fakeDrive) Revoke(context.Context, docsbot.UserID) error { return nil }

func (fakeDrive) Disconnect(context.Context, docsbot.UserID) error { return nil }

type fakeFiles struct{ err error }

func (f fakeFiles) Download(context.Context, string) ([]byte, string, error) {
	return []byte("data"), "application/pdf", f.err
}

type spyNotifier struct {
	mu       sync.Mutex
	backedUp []docsbot.DocumentID
}

func (s *spyNotifier) UploadOutcome(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backedUp = append(s.backedUp, id)
	return nil
}
func (*spyNotifier) Reconnect(context.Context, docsbot.UserID) error { return nil }

func (s *spyNotifier) seen() []docsbot.DocumentID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]docsbot.DocumentID(nil), s.backedUp...)
}

// runUpload enqueues one saved document through the real queue and returns its
// repo, id and the spy once the worker is running.
func runUpload(t *testing.T, files docsbot.FileSource) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier) {
	t.Helper()
	return runUploadWithLog(t, files, fakeDrive{}, slog.New(slog.DiscardHandler))
}

// runUploadWithLog is runUpload with a caller-supplied logger.
func runUploadWithLog(t *testing.T, files docsbot.FileSource, drive docsbot.Drive, log *slog.Logger) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier) {
	t.Helper()
	pool, user, id, spy, enq, queue := setupUpload(t, files, drive, log)
	startQueue(t, queue)
	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	return pool, user, id, spy
}

// setupUpload seeds a saved document and wires the queue, enqueuer and spy
// without starting the queue.
func setupUpload(t *testing.T, files docsbot.FileSource, drive docsbot.Drive, log *slog.Logger) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier, enq *jobs.Enqueuer, queue *pjobs.Client) {
	t.Helper()
	ctx := t.Context()
	pool = dbtest.New(t)
	repo := postgres.NewRepo(pool)

	var uid string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	var did string
	require.NoError(t, pool.QueryRow(ctx,
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id)
		 values ($1::uuid, 'saved', 'document', 'other', 'ПаспортSECRET.pdf', 'file', 'uniq') returning id::text`, uid).Scan(&did))
	user, id = docsbot.UserID(uid), docsbot.DocumentID(did)

	workers := river.NewWorkers()
	bind := jobs.RegisterUploadDocument(workers, log)
	queue, err := pjobs.New(pool, log, workers, map[string]river.QueueConfig{jobs.UploadQueue: {MaxWorkers: 1}})
	require.NoError(t, err)
	enq = jobs.NewEnqueuer(repo, queue)
	svc := docsbot.NewService(repo, drive, files, enq, repo, repo, docsbot.WithSyncs(enq))
	spy = &spyNotifier{}
	bind(svc, spy)
	return pool, user, id, spy, enq, queue
}

// startQueue starts the queue and stops it when the test ends.
func startQueue(t *testing.T, queue *pjobs.Client) {
	t.Helper()
	require.NoError(t, queue.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, queue.Stop(stopCtx))
	})
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
	require.Eventually(t, func() bool { return len(spy.seen()) == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, id, spy.seen()[0], "outcome is reported so the status line shows the file stays in Telegram")
}

// countingDrive counts Upload calls.
type countingDrive struct {
	fakeDrive
	mu    sync.Mutex
	calls int
}

func (d *countingDrive) Upload(ctx context.Context, u docsbot.UserID, up docsbot.DriveUpload) (docsbot.DriveFile, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return d.fakeDrive.Upload(ctx, u, up)
}

func (d *countingDrive) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func TestEnqueueUpload_dedupsWhileQueued(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, slog.New(slog.DiscardHandler))

	// Queue is not started, so the first job stays available and the second is a duplicate.
	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	startQueue(t, queue)

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return len(spy.seen()) >= 1 }, 5*time.Second, 50*time.Millisecond)
	// Give a wrongly inserted second job time to run before asserting.
	time.Sleep(time.Second)
	require.Equal(t, 1, drive.count())
	require.Len(t, spy.seen(), 1)
}

func TestUploadDocument_deletedWhileQueuedFinishesSilently(t *testing.T) {
	drive := &countingDrive{}
	var logs syncBuffer
	log := debugLogger(&logs)
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, log)

	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	_, err := pool.Exec(t.Context(), `delete from documents where id = $1::uuid`, string(id))
	require.NoError(t, err)
	startQueue(t, queue)

	// The job finishes without a retry; wait for the noop log line.
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "upload noop") },
		30*time.Second, 100*time.Millisecond)
	time.Sleep(time.Second)
	require.Zero(t, drive.count())
	require.Empty(t, spy.seen())
	require.NotContains(t, logs.String(), "upload retry")
}

// disconnectedDrive fails every upload the way a user-disconnected Drive does.
type disconnectedDrive struct{ fakeDrive }

func (disconnectedDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{}, docsbot.ErrDriveDisconnected
}

func TestUploadDocument_userDisconnectFinishesSilently(t *testing.T) {
	var logs syncBuffer
	pool, _, id, spy := runUploadWithLog(t, fakeFiles{}, disconnectedDrive{}, debugLogger(&logs))

	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "upload noop") },
		30*time.Second, 100*time.Millisecond)
	time.Sleep(time.Second)
	status, _ := backupState(t, pool, id)
	require.Equal(t, "none", status)
	require.Empty(t, spy.seen())
	require.NotContains(t, logs.String(), "access revoked")
	require.NotContains(t, logs.String(), "reconnect")
	require.NotContains(t, logs.String(), "upload retry")
}
