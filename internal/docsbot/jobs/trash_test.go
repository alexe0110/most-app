package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type fakeTrasher struct {
	err      error
	user     docsbot.UserID
	remoteID string
}

func (t *fakeTrasher) TrashRemote(_ context.Context, user docsbot.UserID, remoteID string) error {
	t.user, t.remoteID = user, remoteID
	return t.err
}

func trashJob(attempt, maxAttempts int) *river.Job[StorageTrashArgs] {
	return &river.Job[StorageTrashArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   StorageTrashArgs{UserID: "u1", RemoteID: "remote-1", Provider: "google"},
	}
}

// The user is told the cloud file stays only when the worker gives up on it:
// no access, or no attempts left. A transient error is River's to retry.
func TestTrashWorker_work(t *testing.T) {
	boom := errors.New("drive 503")
	tests := []struct {
		name     string
		err      error
		attempt  int
		wantErr  error
		wantKept int
		wantLog  string
	}{
		{"trashed", nil, 1, nil, 0, "remote file trashed"},
		{"revoked access: file stays, not retried", docsbot.ErrDriveRevoked, 1, nil, 1, "remote trash skipped: no drive access"},
		{"disconnected by user: file stays, not retried", docsbot.ErrDriveDisconnected, 1, nil, 1, "remote trash skipped: no drive access"},
		{"early failure is retried silently", boom, 1, boom, 0, "trash retry"},
		{"last attempt tells the user and still fails the job", boom, 3, boom, 1, "remote trash failed permanently"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			svc := &fakeTrasher{err: tt.err}
			notify := &spyReconnect{}
			w := &trashWorker{svc: svc, notify: notify, log: log}

			err := w.Work(t.Context(), trashJob(tt.attempt, 3))

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, "trash remote")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, docsbot.UserID("u1"), svc.user)
			require.Equal(t, "remote-1", svc.remoteID)
			require.Equal(t, tt.wantKept, notify.kept)
			require.Zero(t, notify.outcome+notify.syncOutcome+notify.reconnect, "only the kept-file notice")
			out := buf.String()
			require.Contains(t, out, `"msg":"`+tt.wantLog+`"`)
			// The remote id is a Drive file id: it must not be logged.
			require.NotContains(t, out, "remote-1")
			requireNoLeaks(t, out)
		})
	}
}

// A failed notice never fails the job: for a revoked grant a retry cannot
// succeed, and the file is already left behind.
func TestTrashWorker_notifyErrorIsLoggedNotReturned(t *testing.T) {
	log, buf := debugJSONLog()
	w := &trashWorker{svc: &fakeTrasher{err: docsbot.ErrDriveRevoked}, notify: failingNotifier{}, log: log}

	require.NoError(t, w.Work(t.Context(), trashJob(1, 3)))

	out := buf.String()
	require.Contains(t, out, `"msg":"notify user failed"`)
	require.Contains(t, out, `"notice":"RemoteKept"`)
}
