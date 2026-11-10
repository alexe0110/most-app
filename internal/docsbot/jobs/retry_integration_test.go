//go:build integration

package jobs_test

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
)

// Several simultaneous taps on "Повторить" for a failed document must claim it
// once, queue one job and upload the file once.
func TestRetryBackup_simultaneousTapsUploadOnce(t *testing.T) {
	drive := &countingDrive{}
	pool, user, id, spy, enq, queue := setupUpload(t, fakeFiles{}, drive, slog.New(slog.DiscardHandler))
	repo := postgres.NewRepo(pool)
	svc := docsbot.NewService(repo, drive, fakeFiles{}, enq, repo, repo, enq)
	_, err := pool.Exec(t.Context(), `update documents set backup_status = 'failed' where id = $1::uuid`, string(id))
	require.NoError(t, err)

	const taps = 10
	errs := make([]error, taps)
	var wg sync.WaitGroup
	for i := range taps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = svc.RetryBackup(t.Context(), user, id)
		}()
	}
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, docsbot.ErrNotRetryable)
	}
	require.Equal(t, 1, ok)

	startQueue(t, queue)
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "backed_up"
	}, 30*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool { return len(spy.seen()) >= 1 }, 5*time.Second, 50*time.Millisecond)
	time.Sleep(time.Second) // let a wrongly inserted second job run before asserting
	require.Equal(t, 1, drive.count())

	// A stale button on the now backed-up document is a quiet no-op.
	require.ErrorIs(t, svc.RetryBackup(t.Context(), user, id), docsbot.ErrNotRetryable)
	status, _ := backupState(t, pool, id)
	require.Equal(t, "backed_up", status)
}
