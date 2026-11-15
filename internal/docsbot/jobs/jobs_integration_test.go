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
	return docsbot.DriveFile{ID: "drive-1"}, nil
}
func (fakeDrive) Rename(context.Context, docsbot.UserID, string, string) error { return nil }

func (fakeDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error { return nil }

func (fakeDrive) Trash(context.Context, docsbot.UserID, string) error { return nil }

func (fakeDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	return true, nil
}

func (fakeDrive) Replace(context.Context, docsbot.UserID, string, string, []byte) error { return nil }

func (fakeDrive) Revoke(context.Context, docsbot.UserID) error { return nil }

func (fakeDrive) Disconnect(context.Context, docsbot.UserID) error { return nil }

type fakeFiles struct{}

func (fakeFiles) Download(context.Context, string) ([]byte, string, error) {
	return []byte("data"), "application/pdf", nil
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
func runUpload(t *testing.T) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier) {
	t.Helper()
	pool, user, id, spy, enq, queue := setupUpload(t, fakeDrive{}, slog.New(slog.DiscardHandler))
	startQueue(t, queue)
	require.NoError(t, enqueueUpload(t.Context(), pool, enq, user, id))
	return pool, user, id, spy
}

// enqueueUpload puts the saved document in the state the claim paths leave it in
// (backup pending) and queues its upload, unique among live jobs.
func enqueueUpload(ctx context.Context, pool *pgxpool.Pool, docs *postgres.Documents, user docsbot.UserID, id docsbot.DocumentID) error {
	if _, err := pool.Exec(ctx, `update documents set backup_status = 'pending' where id = $1::uuid`, string(id)); err != nil {
		return err
	}
	return docs.RequeueUpload(ctx, user, id)
}

// newService wires a Service over docs and the queue that docs inserts its jobs into.
func newService(pool *pgxpool.Pool, docs *postgres.Documents, jq *jobs.Queue, drive docsbot.Drive) *docsbot.Service {
	return docsbot.NewService(docsbot.Ports{
		Repo: docs, Drive: drive, Files: fakeFiles{}, Uploads: docs, Jobs: jq,
		Folders: postgres.NewFolderCache(pool), Status: postgres.NewStatusMessages(pool), Syncs: docs, Events: postgres.NewEvents(pool),
	})
}

// setupUpload seeds a saved document and wires the queue, enqueuer and spy
// without starting the queue.
func setupUpload(t *testing.T, drive docsbot.Drive, log *slog.Logger) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, spy *spyNotifier, enq *postgres.Documents, queue *pjobs.Client) {
	t.Helper()
	ctx := t.Context()
	pool = dbtest.New(t)

	var uid string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	var did string
	require.NoError(t, pool.QueryRow(ctx,
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id)
		 values ($1::uuid, 'saved', 'document', 'other', 'ПаспортSECRET.pdf', 'file', 'uniq') returning id::text`, uid).Scan(&did))
	user, id = docsbot.UserID(uid), docsbot.DocumentID(did)

	inserter, err := pjobs.NewInserter(pool, log)
	require.NoError(t, err)
	jq := jobs.NewQueue(inserter)
	enq = postgres.NewDocuments(pool, jq)
	svc := newService(pool, enq, jq, drive)
	spy = &spyNotifier{}
	workers := river.NewWorkers()
	jobs.RegisterUploadDocument(workers, log, svc, spy)
	queue, err = pjobs.New(pool, log, workers, map[string]river.QueueConfig{jobs.UploadQueue: {MaxWorkers: 1}})
	require.NoError(t, err)
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

// awaitQueueDrained waits until no job is queued, running or waiting for a
// retry. Negative assertions ("it must not run again") go after it so they see
// the final state instead of racing a job that is still in flight.
func awaitQueueDrained(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	require.Eventually(t, func() bool {
		var live int
		err := pool.QueryRow(t.Context(),
			`select count(*) from river_job where state in ('available', 'pending', 'running', 'retryable', 'scheduled')`).Scan(&live)
		return err == nil && live == 0
	}, 10*time.Second, 50*time.Millisecond, "the queue must drain")
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
	pool, _, id, spy := runUpload(t)

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 10*time.Second, 100*time.Millisecond)
	_, driveID := backupState(t, pool, id)
	require.Equal(t, "drive-1", driveID)
	require.Eventually(t, func() bool { return len(spy.seen()) == 1 }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, id, spy.seen()[0])
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

func TestUploadJob_dedupsWhileQueued(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, spy, enq, queue := setupUpload(t, drive, slog.New(slog.DiscardHandler))

	// Queue is not started, so the first job stays available and the second is a duplicate.
	require.NoError(t, enqueueUpload(t.Context(), pool, enq, user, id))
	require.NoError(t, enqueueUpload(t.Context(), pool, enq, user, id))
	startQueue(t, queue)

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 10*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return len(spy.seen()) >= 1 }, 5*time.Second, 50*time.Millisecond)
	awaitQueueDrained(t, pool) // a wrongly inserted second job would have run by now
	require.Equal(t, 1, drive.count())
	require.Len(t, spy.seen(), 1)
}
