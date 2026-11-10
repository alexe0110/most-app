//go:build integration

package jobs_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

// setupSweep is setupUpload plus a service to sweep with. It puts the seeded
// document into the given backup_status and age, and creates the user's Drive
// integration with integrationStatus ("" means no integration row). start runs
// the upload queue.
func setupSweep(t *testing.T, drive docsbot.Drive, backup, age, integrationStatus string) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, svc *docsbot.Service, spy *spyNotifier, enq *jobs.Enqueuer, start func()) {
	t.Helper()
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, slog.New(slog.DiscardHandler))
	repo := postgres.NewRepo(pool)
	svc = docsbot.NewService(repo, drive, fakeFiles{}, enq, repo, repo, docsbot.WithSyncs(enq))

	ctx := t.Context()
	_, err := pool.Exec(ctx, `update documents set backup_status = $2, updated_at = now() - $3::interval where id = $1::uuid`,
		string(id), backup, age)
	require.NoError(t, err)
	if integrationStatus != "" {
		_, err = pool.Exec(ctx, `insert into integrations (user_id, bot, provider, status) values ($1::uuid, 'docs', 'google', $2)`,
			string(user), integrationStatus)
		require.NoError(t, err)
	}
	return pool, user, id, svc, spy, enq, func() { startQueue(t, queue) }
}

func TestSweepStuckUploads_requeuesStaleAndBacksUp(t *testing.T) {
	pool, _, id, svc, _, _, start := setupSweep(t, &countingDrive{}, "uploading", "31 minutes", "active")

	res, err := svc.SweepStuckUploads(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, res.Found)
	require.Equal(t, 1, res.Requeued)
	require.Empty(t, res.Failed)

	start()
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
}

func TestSweepStuckUploads_liveJobDedups(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, svc, _, enq, start := setupSweep(t, drive, "none", "0 seconds", "active")

	// The queue is not started, so the enqueued job stays live.
	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	_, err := pool.Exec(t.Context(), `update documents set updated_at = now() - interval '31 minutes' where id = $1::uuid`, string(id))
	require.NoError(t, err)

	res, err := svc.SweepStuckUploads(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, res.Requeued)

	start()
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	// Give a wrongly inserted second job time to run before asserting.
	time.Sleep(time.Second)
	require.Equal(t, 1, drive.count())
}

func TestSweepStuckUploads_failsOldUpload(t *testing.T) {
	pool, _, id, svc, _, _, _ := setupSweep(t, &countingDrive{}, "uploading", "25 hours", "active")

	res, err := svc.SweepStuckUploads(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, res.Found)
	require.Zero(t, res.Requeued)
	require.Len(t, res.Failed, 1)
	require.Equal(t, id, res.Failed[0].ID)
	status, _ := backupState(t, pool, id)
	require.Equal(t, "failed", status)
}

func TestSweepStuckUploads_skips(t *testing.T) {
	tests := []struct {
		name        string
		age         string
		integration string
	}{
		{name: "fresh upload", age: "0 seconds", integration: "active"},
		{name: "revoked integration", age: "31 minutes", integration: "revoked"},
		{name: "no integration", age: "31 minutes", integration: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, _, id, svc, _, _, _ := setupSweep(t, &countingDrive{}, "uploading", tt.age, tt.integration)

			res, err := svc.SweepStuckUploads(t.Context())
			require.NoError(t, err)
			require.Zero(t, res.Found)
			require.Zero(t, res.Requeued)
			require.Empty(t, res.Failed)
			status, _ := backupState(t, pool, id)
			require.Equal(t, "uploading", status)
		})
	}
}

func TestSweepWorker_notifiesFailed(t *testing.T) {
	pool, _, id, svc, spy, _, _ := setupSweep(t, &countingDrive{}, "uploading", "25 hours", "active")

	workers := river.NewWorkers()
	bind, periodic := jobs.RegisterSweepUploads(workers, slog.New(slog.DiscardHandler))
	require.NotNil(t, periodic)
	bind(svc, spy)
	queue, err := pjobs.New(pool, slog.New(slog.DiscardHandler), workers, nil, periodic)
	require.NoError(t, err)
	startQueue(t, queue)

	require.Eventually(t, func() bool { return len(spy.seen()) == 1 }, 30*time.Second, 100*time.Millisecond)
	require.Equal(t, id, spy.seen()[0])
	status, _ := backupState(t, pool, id)
	require.Equal(t, "failed", status)
}
