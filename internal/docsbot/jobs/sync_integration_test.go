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

// cloudDrive records sync calls; Rename can block to hold a sync "running".
type cloudDrive struct {
	fakeDrive
	mu       sync.Mutex
	renames  []string
	trashes  []string
	trashErr error
	entered  chan struct{} // closed-over signal: first Rename started
	release  chan struct{} // when non-nil the first Rename waits for it
	once     sync.Once
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

func (d *cloudDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	return true, nil
}

func (d *cloudDrive) Trash(_ context.Context, _ docsbot.UserID, remoteID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.trashes = append(d.trashes, remoteID)
	return d.trashErr
}

func (d *cloudDrive) renamed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.renames...)
}

func (d *cloudDrive) trashed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.trashes...)
}

type syncSpy struct {
	mu                         sync.Mutex
	outcomes, reconnects, kept int
}

func (s *syncSpy) SyncOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes++
	return nil
}

func (s *syncSpy) Reconnect(context.Context, docsbot.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconnects++
	return nil
}

func (s *syncSpy) RemoteKept(context.Context, docsbot.UserID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kept++
	return nil
}

func (s *syncSpy) counts() (outcomes, reconnects, kept int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outcomes, s.reconnects, s.kept
}

type syncEnv struct {
	pool  *pgxpool.Pool
	svc   *docsbot.Service
	queue *pjobs.Client
	spy   *syncSpy
	user  docsbot.UserID
	id    docsbot.DocumentID
}

// setupSync seeds a backed-up document and wires the sync and trash workers
// without starting the queue.
func setupSync(t *testing.T, drive docsbot.Drive) syncEnv {
	t.Helper()
	ctx := t.Context()
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	log := slog.New(slog.DiscardHandler)

	var uid, did string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (1) returning id::text`).Scan(&uid))
	require.NoError(t, pool.QueryRow(ctx,
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status, drive_file_id, mime_type)
		 values ($1::uuid, 'saved', 'document', 'other', 'Старое', 'file', 'uniq', 'backed_up', 'remote-1', 'application/pdf') returning id::text`, uid).Scan(&did))

	workers := river.NewWorkers()
	bindSync := jobs.RegisterStorageSync(workers, log)
	bindTrash := jobs.RegisterStorageTrash(workers, log)
	queue, err := pjobs.New(pool, log, workers, map[string]river.QueueConfig{jobs.UploadQueue: {MaxWorkers: 1}})
	require.NoError(t, err)
	enq := jobs.NewEnqueuer(repo, queue)
	svc := docsbot.NewService(repo, drive, fakeFiles{}, enq, repo, repo, enq)
	spy := &syncSpy{}
	bindSync(svc, spy)
	bindTrash(svc, spy)
	return syncEnv{pool: pool, svc: svc, queue: queue, spy: spy, user: docsbot.UserID(uid), id: docsbot.DocumentID(did)}
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

	require.Eventually(t, func() bool { return e.syncStatus(t) == "ok" }, 30*time.Second, 100*time.Millisecond)
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
	require.Eventually(t, func() bool { return e.syncStatus(t) == "ok" }, 30*time.Second, 100*time.Millisecond)
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
	case <-time.After(30 * time.Second):
		t.Fatal("sync did not start")
	}
	require.Equal(t, 1, e.jobCount(t, "storage_sync", "running"))

	_, err = e.svc.Rename(t.Context(), e.user, e.id, "Второе")
	require.NoError(t, err)
	require.Equal(t, 1, e.jobCount(t, "storage_sync", "available"), "follow-up queued next to the running job")
	close(drive.release)

	require.Eventually(t, func() bool {
		return e.syncStatus(t) == "ok" && len(drive.renamed()) >= 2
	}, 30*time.Second, 100*time.Millisecond)
	names := drive.renamed()
	require.Equal(t, "Второе.pdf", names[len(names)-1], "the last sync applies the newest title")
}

func TestStorageTrash_deleteTrashesRemoteFile(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}}
	e := setupSync(t, drive)

	got, err := e.svc.Delete(t.Context(), e.user, e.id)
	require.NoError(t, err)
	require.Equal(t, docsbot.Deleted{}, got)
	require.Equal(t, 1, e.jobCount(t, "storage_trash", ""), "queued with the row deletion")

	startQueue(t, e.queue)
	require.Eventually(t, func() bool { return len(drive.trashed()) == 1 }, 30*time.Second, 100*time.Millisecond)
	require.Equal(t, []string{"remote-1"}, drive.trashed())
	_, _, kept := e.spy.counts()
	require.Zero(t, kept)

	// Double tap on delete: the row is gone, no second job.
	_, err = e.svc.Delete(t.Context(), e.user, e.id)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	require.Equal(t, 1, e.jobCount(t, "storage_trash", ""))
}

func TestStorageTrash_revokedAccessTellsUserTheFileStays(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}, trashErr: docsbot.ErrDriveRevoked}
	e := setupSync(t, drive)
	_, err := e.svc.Delete(t.Context(), e.user, e.id)
	require.NoError(t, err)

	startQueue(t, e.queue)

	require.Eventually(t, func() bool { _, _, kept := e.spy.counts(); return kept == 1 }, 30*time.Second, 100*time.Millisecond)
	require.Equal(t, 1, len(drive.trashed()), "terminal: not retried")
}

func TestStorageSync_revokedMarksFailedWithReasonAndAsksToReconnect(t *testing.T) {
	drive := &revokedMoveDrive{cloudDrive: cloudDrive{fakeDrive: fakeDrive{}}}
	e := setupSync(t, drive)
	startQueue(t, e.queue)

	_, err := e.svc.Rename(t.Context(), e.user, e.id, "Новое")
	require.NoError(t, err)

	require.Eventually(t, func() bool { return e.syncStatus(t) == "failed" }, 30*time.Second, 100*time.Millisecond)
	var reason *string
	require.NoError(t, e.pool.QueryRow(t.Context(), `select sync_reason from documents where id = $1::uuid`, string(e.id)).Scan(&reason))
	require.NotNil(t, reason)
	require.Equal(t, docsbot.SyncReasonRevoked, *reason)
	require.Eventually(t, func() bool { o, r, _ := e.spy.counts(); return o == 1 && r == 1 }, 5*time.Second, 50*time.Millisecond)
}

type revokedMoveDrive struct{ cloudDrive }

func (d *revokedMoveDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error {
	return docsbot.ErrDriveRevoked
}

// A sync job for a document that was deleted meanwhile finishes silently.
func TestStorageSync_deletedDocumentIsNoop(t *testing.T) {
	drive := &cloudDrive{fakeDrive: fakeDrive{}}
	e := setupSync(t, drive)
	_, err := e.svc.Rename(t.Context(), e.user, e.id, "Новое")
	require.NoError(t, err)
	_, err = e.pool.Exec(t.Context(), `delete from documents where id = $1::uuid`, string(e.id))
	require.NoError(t, err)

	startQueue(t, e.queue)

	require.Eventually(t, func() bool { return e.jobCount(t, "storage_sync", "completed") == 1 }, 30*time.Second, 100*time.Millisecond)
	require.Empty(t, drive.renamed())
}
