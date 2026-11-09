//go:build integration

package jobs_test

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Many simultaneous taps / duplicate updates for one document give one upload.
func TestEnqueueUpload_concurrentSameDocumentUploadsOnce(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, discardLog())

	const taps = 10
	var wg sync.WaitGroup
	errs := make(chan error, taps)
	for range taps {
		wg.Go(func() { errs <- enq.EnqueueUpload(t.Context(), user, id) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	startQueue(t, queue)

	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	time.Sleep(time.Second)
	require.Equal(t, 1, drive.count())
	require.Len(t, spy.seen(), 1)
}

// "Retry" after a failed upload: the earlier job is finished, so a new one is created.
func TestEnqueueUpload_retryAfterFailedRuns(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, _, enq, queue := setupUpload(t, fakeFiles{}, drive, discardLog())
	startQueue(t, queue)

	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	// backed_up is written inside Work; River marks the job Completed only after Work returns.
	// Until then the job is still Running and a re-enqueue is deduplicated (unique ByState
	// includes Running). Let the job settle; do not remove this pause.
	time.Sleep(2 * time.Second)

	_, err := pool.Exec(t.Context(), `update documents set backup_status = 'failed', drive_file_id = null where id = $1::uuid`, string(id))
	require.NoError(t, err)
	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))

	require.Eventually(t, func() bool { return drive.count() == 2 }, 30*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
}

// A skipped duplicate must leave the document status as it was.
func TestEnqueueUpload_duplicateKeepsStatus(t *testing.T) {
	pool, user, id, _, enq, _ := setupUpload(t, fakeFiles{}, &countingDrive{}, discardLog())

	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	_, err := pool.Exec(t.Context(), `update documents set backup_status = 'uploading' where id = $1::uuid`, string(id))
	require.NoError(t, err)

	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))

	status, _ := backupState(t, pool, id)
	require.Equal(t, "uploading", status)
}

// Disconnect with a queued job and documents in pending/uploading: the job is
// removed, the documents fall back to none, nothing is uploaded afterwards.
func TestDisconnect_resetsQueuedAndUploading(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, svc, spy, enq, start := setupSweep(t, drive, "none", "0 seconds", "active")

	require.NoError(t, enq.EnqueueUpload(t.Context(), user, id))
	var uploadingID string
	require.NoError(t, pool.QueryRow(t.Context(),
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status)
		 values ($1::uuid, 'saved', 'document', 'other', 't', 'f2', 'u2', 'uploading') returning id::text`, string(user)).Scan(&uploadingID))

	require.NoError(t, svc.Disconnect(t.Context(), user))
	start()
	time.Sleep(2 * time.Second)

	for _, d := range []docsbot.DocumentID{id, docsbot.DocumentID(uploadingID)} {
		status, _ := backupState(t, pool, d)
		require.Equal(t, "none", status)
	}
	require.Zero(t, drive.count())
	require.Empty(t, spy.seen())
}
