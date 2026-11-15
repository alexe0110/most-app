//go:build integration

package jobs_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
)

// Disconnect with a queued job and documents in pending/uploading: the job is
// removed, the documents fall back to none, nothing is uploaded afterwards.
func TestDisconnect_resetsQueuedAndUploading(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, svc, spy, enq, start := setupSweep(t, drive, "none", "0 seconds", "active")

	require.NoError(t, enqueueUpload(t.Context(), pool, enq, user, id))
	var uploadingID string
	require.NoError(t, pool.QueryRow(t.Context(),
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status)
		 values ($1::uuid, 'saved', 'document', 'other', 't', 'f2', 'u2', 'uploading') returning id::text`, string(user)).Scan(&uploadingID))

	require.NoError(t, svc.Disconnect(t.Context(), user))
	start()
	awaitQueueDrained(t, pool) // a job left behind by Disconnect would have uploaded by now

	for _, d := range []docsbot.DocumentID{id, docsbot.DocumentID(uploadingID)} {
		status, _ := backupState(t, pool, d)
		require.Equal(t, "none", status)
	}
	require.Zero(t, drive.count())
	require.Empty(t, spy.seen())
}

// setupSweep is setupUpload plus a service to sweep with. It puts the seeded
// document into the given backup_status and age, and creates the user's Drive
// integration with integrationStatus ("" means no integration row). start runs
// the upload queue.
func setupSweep(t *testing.T, drive docsbot.Drive, backup, age, integrationStatus string) (pool *pgxpool.Pool, user docsbot.UserID, id docsbot.DocumentID, svc *docsbot.Service, spy *spyNotifier, enq *postgres.Documents, start func()) {
	t.Helper()
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, slog.New(slog.DiscardHandler))
	svc = newService(pool, enq, jobs.NewQueue(queue.Inserter), drive, fakeFiles{})

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
	}, 10*time.Second, 100*time.Millisecond)
}

func TestSweepStuckUploads_liveJobDedups(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, svc, _, enq, start := setupSweep(t, drive, "none", "0 seconds", "active")

	// The queue is not started, so the enqueued job stays live.
	require.NoError(t, enqueueUpload(t.Context(), pool, enq, user, id))
	_, err := pool.Exec(t.Context(), `update documents set updated_at = now() - interval '31 minutes' where id = $1::uuid`, string(id))
	require.NoError(t, err)

	res, err := svc.SweepStuckUploads(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, res.Requeued)

	start()
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 10*time.Second, 100*time.Millisecond)
	awaitQueueDrained(t, pool) // a wrongly inserted second job would have run by now
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
