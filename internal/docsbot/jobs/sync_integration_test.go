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

// cloudDrive records renames and trashes; Rename can block to hold a sync "running".
type cloudDrive struct {
	fakeDrive
	mu      sync.Mutex
	renames []string
	trashes []string
	entered chan struct{} // closed-over signal: first Rename started
	release chan struct{} // when non-nil the first Rename waits for it
	once    sync.Once
}

func (d *cloudDrive) Rename(_ context.Context, _ docsbot.UserID, _, name string) error {
	d.once.Do(func() {
		if d.entered != nil {
			close(d.entered)
		}
		if d.release != nil {
			<-d.release
		}
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	d.renames = append(d.renames, name)
	return nil
}

func (d *cloudDrive) Trash(_ context.Context, _ docsbot.UserID, remoteID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trashes = append(d.trashes, remoteID)
	return nil
}

func (d *cloudDrive) trashed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.trashes...)
}

func (d *cloudDrive) renamed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.renames...)
}

// quietSyncNotifier drops the notices: these tests check the queue, not what
// the user is told (covered by the sync worker unit tests).
type quietSyncNotifier struct{}

func (quietSyncNotifier) SyncOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return nil
}

func (quietSyncNotifier) Reconnect(context.Context, docsbot.UserID) error { return nil }

func (quietSyncNotifier) RemoteKept(context.Context, docsbot.UserID) error { return nil }

type syncEnv struct {
	pool  *pgxpool.Pool
	svc   *docsbot.Service
	queue *pjobs.Client
	user  docsbot.UserID
	id    docsbot.DocumentID
}

// setupSync seeds a backed-up document and wires the sync and trash workers
// without starting the queue.
func setupSync(t *testing.T, drive docsbot.Drive) syncEnv {
	t.Helper()
	ctx := t.Context()
	pool := dbtest.New(t)
	log := slog.New(slog.DiscardHandler)

	var uid, did string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	require.NoError(t, pool.QueryRow(ctx,
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status, drive_file_id, mime_type)
		 values ($1::uuid, 'saved', 'document', 'other', 'Старое', 'file', 'uniq', 'backed_up', 'remote-1', 'application/pdf') returning id::text`, uid).Scan(&did))

	inserter, err := pjobs.NewInserter(pool, log)
	require.NoError(t, err)
	jq := jobs.NewQueue(inserter)
	svc := newService(pool, postgres.NewDocuments(pool, jq), jq, drive)
	workers := river.NewWorkers()
	jobs.RegisterStorageSync(workers, log, svc, quietSyncNotifier{})
	jobs.RegisterStorageTrash(workers, log, svc, quietSyncNotifier{})
	queue, err := pjobs.New(pool, log, workers, map[string]river.QueueConfig{jobs.UploadQueue: {MaxWorkers: 1}})
	require.NoError(t, err)
	return syncEnv{pool: pool, svc: svc, queue: queue, user: docsbot.UserID(uid), id: docsbot.DocumentID(did)}
}

func (e syncEnv) syncStatus(t *testing.T) string {
	t.Helper()
	var s string
	require.NoError(t, e.pool.QueryRow(t.Context(), `select sync_status from documents where id = $1::uuid`, string(e.id)).Scan(&s))
	return s
}

func (e syncEnv) jobCount(t *testing.T, kind, state string) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`select count(*) from river_job where kind = $1 and ($2 = '' or state::text = $2)`, kind, state).Scan(&n))
	return n
}

func TestStorageSync_renameRunsAndMarksOK(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}}
	e := setupSync(t, drive)
	startQueue(t, e.queue)

	_, err := e.svc.Rename(t.Context(), e.user, e.id, "Договор")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return e.syncStatus(t) == "ok" }, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, []string{"Договор.pdf"}, drive.renamed())
}

// Changes made before the worker picks the jobs up queue extra jobs, but the
// worker applies the latest state once; the rest are no-ops.
func TestStorageSync_queuedChangesApplyLatestOnce(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}}
	e := setupSync(t, drive)

	for _, title := range []string{"Первое", "Второе", "Третье"} {
		_, err := e.svc.Rename(t.Context(), e.user, e.id, title)
		require.NoError(t, err)
	}
	_, err := e.svc.Recategorize(t.Context(), e.user, e.id, docsbot.CatPassport)
	require.NoError(t, err)

	startQueue(t, e.queue)
	require.Eventually(t, func() bool { return e.syncStatus(t) == "ok" }, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, []string{"Третье.pdf"}, drive.renamed())
}

// A rename that arrives while a sync is running must queue a follow-up job
// (the running one may have read the old title), and the end state matches the
// newest title.
func TestStorageSync_changeDuringRunningSyncQueuesFollowUp(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}, entered: make(chan struct{}), release: make(chan struct{})}
	e := setupSync(t, drive)
	startQueue(t, e.queue)

	_, err := e.svc.Rename(t.Context(), e.user, e.id, "Первое")
	require.NoError(t, err)
	select {
	case <-drive.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not start")
	}
	require.Equal(t, 1, e.jobCount(t, "storage_sync", "running"))

	_, err = e.svc.Rename(t.Context(), e.user, e.id, "Второе")
	require.NoError(t, err)
	require.Equal(t, 1, e.jobCount(t, "storage_sync", "available"), "follow-up queued next to the running job")
	close(drive.release)

	require.Eventually(t, func() bool {
		return e.syncStatus(t) == "ok" && len(drive.renamed()) >= 2
	}, 10*time.Second, 100*time.Millisecond)
	names := drive.renamed()
	require.Equal(t, "Второе.pdf", names[len(names)-1], "the last sync applies the newest title")
}

// Deleting a backed-up document queues the trash job with the row deletion, the
// worker trashes that very Drive file, and a double tap queues nothing more.
func TestStorageTrash_deleteTrashesRemoteFile(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}}
	e := setupSync(t, drive)

	got, err := e.svc.Delete(t.Context(), e.user, e.id)
	require.NoError(t, err)
	require.Equal(t, docsbot.Deleted{}, got)
	require.Equal(t, 1, e.jobCount(t, "storage_trash", ""), "queued with the row deletion")

	startQueue(t, e.queue)
	require.Eventually(t, func() bool { return len(drive.trashed()) == 1 }, 10*time.Second, 100*time.Millisecond)
	require.Equal(t, []string{"remote-1"}, drive.trashed())

	_, err = e.svc.Delete(t.Context(), e.user, e.id)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	awaitQueueDrained(t, e.pool)
	require.Equal(t, 1, e.jobCount(t, "storage_trash", ""), "the double tap queued no second job")
	require.Len(t, drive.trashed(), 1)
}
