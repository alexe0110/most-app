//go:build integration

package jobs_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	"github.com/alexe0110/most-app/internal/platform/jobs"
)

// okArgs is a trivial job used to prove enqueue, processing and durability.
type okArgs struct{}

func (okArgs) Kind() string { return "test_ok" }

type okWorker struct {
	river.WorkerDefaults[okArgs]
	done chan struct{}
}

func (w *okWorker) Work(context.Context, *river.Job[okArgs]) error {
	if w.done != nil {
		w.done <- struct{}{}
	}
	return nil
}

// flakyArgs fails on its first two attempts and succeeds on the third, with an
// immediate retry so the test does not wait for the real backoff.
type flakyArgs struct{}

func (flakyArgs) Kind() string { return "test_flaky" }

type flakyWorker struct {
	river.WorkerDefaults[flakyArgs]
	succeeded chan int
}

func (*flakyWorker) NextRetry(*river.Job[flakyArgs]) time.Time { return time.Now() }

func (w *flakyWorker) Work(_ context.Context, job *river.Job[flakyArgs]) error {
	if job.Attempt < 3 {
		return fmt.Errorf("deliberate failure on attempt %d", job.Attempt)
	}
	w.succeeded <- job.Attempt
	return nil
}

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// startedClient builds a client, starts it and stops it on cleanup.
func startedClient(t *testing.T, pool *pgxpool.Pool, workers *river.Workers) *jobs.Client {
	t.Helper()
	c, err := jobs.New(pool, discardLog(), workers)
	require.NoError(t, err)
	require.NoError(t, c.Start(t.Context()))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, c.Stop(ctx))
	})
	return c
}

func countRiverJobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), `select count(*) from river_job`).Scan(&n))
	return n
}

// TestClient_retriesUntilSuccess covers: a job failing twice runs on the third
// attempt.
func TestClient_retriesUntilSuccess(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	succeeded := make(chan int, 1)
	workers := river.NewWorkers()
	river.AddWorker(workers, &flakyWorker{succeeded: succeeded})

	queue := startedClient(t, pool, workers)
	_, err := queue.Insert(ctx, flakyArgs{}, nil)
	require.NoError(t, err)

	select {
	case attempt := <-succeeded:
		require.Equal(t, 3, attempt, "job must succeed on its third attempt")
	case <-time.After(30 * time.Second):
		t.Fatal("flaky job never completed")
	}
}

// TestClient_insertTxRollback covers: a job enqueued in a transaction that
// rolls back never reaches the queue; the same enqueue committed does.
func TestClient_insertTxRollback(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	workers := river.NewWorkers()
	river.AddWorker(workers, &okWorker{})
	queue, err := jobs.New(pool, discardLog(), workers) // not started: we test insertion only
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = queue.InsertTx(ctx, tx, okArgs{}, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.Equal(t, 0, countRiverJobs(t, pool), "rolled-back job must not appear")

	tx2, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = queue.InsertTx(ctx, tx2, okArgs{}, nil)
	require.NoError(t, err)
	require.NoError(t, tx2.Commit(ctx))
	require.Equal(t, 1, countRiverJobs(t, pool), "committed job must appear")
}

// TestClient_restartDoesNotLoseJobs covers: a job inserted by a client that
// never ran (a crash before processing) is picked up by a fresh client.
func TestClient_restartDoesNotLoseJobs(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	// Client A enqueues a job and is never started.
	workersA := river.NewWorkers()
	river.AddWorker(workersA, &okWorker{})
	crashed, err := jobs.New(pool, discardLog(), workersA)
	require.NoError(t, err)
	_, err = crashed.Insert(ctx, okArgs{}, nil)
	require.NoError(t, err)

	// Client B starts on the same database and must process the persisted job.
	done := make(chan struct{}, 1)
	workersB := river.NewWorkers()
	river.AddWorker(workersB, &okWorker{done: done})
	startedClient(t, pool, workersB)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("persisted job was lost across restart")
	}
}

// TestPurgeUsers covers: purge_users deletes a soft-deleted user and cascades
// to its documents, while leaving other users and their documents intact.
func TestPurgeUsers(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()

	keptID := insertUser(t, pool, 1, false)
	delID := insertUser(t, pool, 2, true)
	insertDocument(t, pool, keptID, "kept")
	insertDocument(t, pool, delID, "doomed")

	workers := river.NewWorkers()
	purge := jobs.RegisterPurgeUsers(workers, pool, discardLog())
	queue, err := jobs.New(pool, discardLog(), workers, purge)
	require.NoError(t, err)
	require.NoError(t, queue.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, queue.Stop(stopCtx))
	})

	_, err = queue.Insert(ctx, jobs.PurgeUsersArgs{}, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from users where deleted_at is not null`).Scan(&n); err != nil {
			return false
		}
		return n == 0
	}, 30*time.Second, 100*time.Millisecond, "soft-deleted user must be purged")

	require.Equal(t, 1, countRows(t, pool, `select count(*) from users where id = $1::uuid`, keptID), "kept user survives")
	require.Equal(t, 1, countRows(t, pool, `select count(*) from documents where user_id = $1::uuid`, keptID), "kept user's document survives")
	require.Equal(t, 0, countRows(t, pool, `select count(*) from documents where user_id = $1::uuid`, delID), "deleted user's document cascades")
}

func insertUser(t *testing.T, pool *pgxpool.Pool, tgID int64, deleted bool) string {
	t.Helper()
	deletedAt := "null"
	if deleted {
		deletedAt = "now()"
	}
	var id string
	q := fmt.Sprintf(`insert into users (tg_user_id, deleted_at) values ($1, %s) returning id::text`, deletedAt)
	require.NoError(t, pool.QueryRow(t.Context(), q, tgID).Scan(&id))
	return id
}

func insertDocument(t *testing.T, pool *pgxpool.Pool, userID, uniq string) {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		`insert into documents (user_id, status, category, title, tg_file_id, tg_file_unique_id)
		 values ($1::uuid, 'pending', 'misc', 'title', 'file', $2)`,
		userID, uniq)
	require.NoError(t, err)
}

func countRows(t *testing.T, pool *pgxpool.Pool, query, arg string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), query, arg).Scan(&n))
	return n
}
