//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// syncBuffer is a log sink safe for the worker goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func debugLogger(sink *syncBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func requireNoLeaks(t *testing.T, out string) {
	t.Helper()
	for _, leak := range []string{"ПаспортSECRET", "SECRET", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}

func TestUploadDocument_logsLifecycle(t *testing.T) {
	var sink syncBuffer
	_, user, id, _ := runUploadWithLog(t, fakeFiles{}, fakeDrive{}, debugLogger(&sink))

	require.Eventually(t, func() bool { return strings.Contains(sink.String(), "upload finished") },
		30*time.Second, 50*time.Millisecond)
	out := sink.String()
	require.Contains(t, out, "upload started")
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	require.Contains(t, out, `"user_id":"`+string(user)+`"`)
	require.Contains(t, out, `"job_id"`)
	require.Contains(t, out, `"backup_status":"backed_up"`)
	require.Contains(t, out, `"duration_ms"`)
	require.Contains(t, out, `"category"`)
	requireNoLeaks(t, out)
}

func TestUploadDocument_logsRetry(t *testing.T) {
	var sink syncBuffer
	_, _, id, _ := runUploadWithLog(t, fakeFiles{err: errors.New("telegram down")}, fakeDrive{}, debugLogger(&sink))

	require.Eventually(t, func() bool { return strings.Contains(sink.String(), "upload retry") },
		30*time.Second, 50*time.Millisecond)
	out := sink.String()
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	require.Contains(t, out, `"job_id"`)
	require.Contains(t, out, `"attempt":1`)
	requireNoLeaks(t, out)
}

// revokedDrive fails every upload the way an expired Google grant does.
type revokedDrive struct{ fakeDrive }

func (revokedDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{}, docsbot.ErrDriveRevoked
}

func TestUploadDocument_logsRevoke(t *testing.T) {
	var sink syncBuffer
	pool, user, id, _ := runUploadWithLog(t, fakeFiles{}, revokedDrive{}, debugLogger(&sink))

	require.Eventually(t, func() bool { return strings.Contains(sink.String(), "access revoked") },
		30*time.Second, 50*time.Millisecond)
	out := sink.String()
	require.Contains(t, out, `"level":"WARN"`)
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	require.Contains(t, out, `"user_id":"`+string(user)+`"`)
	require.Contains(t, out, `"job_id"`)
	requireNoLeaks(t, out)
	require.Eventually(t, func() bool {
		status, _ := backupState(t, pool, id)
		return status == "failed"
	}, 10*time.Second, 50*time.Millisecond)
}
