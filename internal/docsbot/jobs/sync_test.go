package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type fakeSyncer struct {
	res     docsbot.SyncResult
	err     error
	markErr error
	marked  bool
	reason  string
}

func (s *fakeSyncer) SyncRemote(context.Context, docsbot.UserID, docsbot.DocumentID) (docsbot.SyncResult, error) {
	return s.res, s.err
}

func (s *fakeSyncer) MarkSyncFailed(_ context.Context, _ docsbot.UserID, _ docsbot.DocumentID, reason string) error {
	s.marked, s.reason = true, reason
	return s.markErr
}

func syncJob(attempt, maxAttempts int) *river.Job[StorageSyncArgs] {
	return &river.Job[StorageSyncArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   StorageSyncArgs{UserID: "u1", DocumentID: "d1"},
	}
}

// Every outcome of one sync attempt maps to what is stored, what the user is
// told and whether River retries.
func TestSyncWorker_work(t *testing.T) {
	boom := errors.New("drive 503")
	tests := []struct {
		name          string
		syncer        fakeSyncer
		attempt       int
		wantErr       error
		wantMarked    bool
		wantReason    string
		wantOutcome   int
		wantReconnect int
		wantLog       string
	}{
		{
			name:   "revoked grant is terminal and asks to reconnect",
			syncer: fakeSyncer{err: fmt.Errorf("move: %w", docsbot.ErrDriveRevoked)}, attempt: 1,
			wantMarked: true, wantReason: docsbot.SyncReasonRevoked, wantOutcome: 1, wantReconnect: 1,
			wantLog: "drive sync: access revoked",
		},
		{
			name:   "early failure is left to River unmarked",
			syncer: fakeSyncer{err: boom}, attempt: 1, wantErr: boom,
			wantLog: "sync retry",
		},
		{
			name:   "last attempt marks failed without a reason and tells the user",
			syncer: fakeSyncer{err: boom}, attempt: 3, wantErr: boom,
			wantMarked: true, wantOutcome: 1,
			wantLog: "drive sync failed permanently",
		},
		{
			name:   "missing remote file tells the user",
			syncer: fakeSyncer{res: docsbot.SyncResult{Missing: true}}, attempt: 1,
			wantOutcome: 1,
			wantLog:     "drive sync: remote file missing",
		},
		{
			name:   "nothing to do stays silent",
			syncer: fakeSyncer{res: docsbot.SyncResult{Noop: true}}, attempt: 1,
			wantLog: "sync noop",
		},
		{
			name:   "synced stays silent",
			syncer: fakeSyncer{}, attempt: 1,
			wantLog: "sync finished",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			notify := &spyReconnect{}
			w := &syncWorker{svc: &tt.syncer, notify: notify, log: log}

			err := w.Work(t.Context(), syncJob(tt.attempt, 3))

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, "sync document")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantMarked, tt.syncer.marked)
			require.Equal(t, tt.wantReason, tt.syncer.reason)
			require.Equal(t, tt.wantOutcome, notify.syncOutcome)
			require.Equal(t, tt.wantReconnect, notify.reconnect)
			require.Zero(t, notify.outcome+notify.kept, "only sync notices")
			out := buf.String()
			require.Contains(t, out, `"msg":"`+tt.wantLog+`"`)
			require.Contains(t, out, `"doc_id":"d1"`)
			require.Contains(t, out, `"user_id":"u1"`)
			requireNoLeaks(t, out)
		})
	}
}

// A failed notice is logged and never fails the job: a retry would redo a sync
// that already ended, only to repeat a best-effort message.
func TestSyncWorker_notifyErrorIsLoggedNotReturned(t *testing.T) {
	tests := []struct {
		name    string
		syncer  fakeSyncer
		attempt int
		notices []string
	}{
		{"revoked", fakeSyncer{err: docsbot.ErrDriveRevoked}, 1, []string{"SyncOutcome", "Reconnect"}},
		{"missing", fakeSyncer{res: docsbot.SyncResult{Missing: true}}, 1, []string{"SyncOutcome"}},
		{"permanent failure", fakeSyncer{err: errors.New("boom")}, 3, []string{"SyncOutcome"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			w := &syncWorker{svc: &tt.syncer, notify: failingNotifier{}, log: log}

			err := w.Work(t.Context(), syncJob(tt.attempt, 3))

			if tt.syncer.err != nil && !errors.Is(tt.syncer.err, docsbot.ErrDriveRevoked) {
				require.Error(t, err, "the sync error itself is still returned")
			} else {
				require.NoError(t, err)
			}
			out := buf.String()
			require.Contains(t, out, `"msg":"notify user failed"`)
			for _, notice := range tt.notices {
				require.Contains(t, out, `"notice":"`+notice+`"`)
			}
			requireNoLeaks(t, out)
		})
	}
}

// A failure to store the failed state is logged; the job still ends as the
// sync outcome dictates and the user is still told.
func TestSyncWorker_markFailureIsLoggedNotReturned(t *testing.T) {
	log, buf := debugJSONLog()
	notify := &spyReconnect{}
	syncer := &fakeSyncer{err: docsbot.ErrDriveRevoked, markErr: errors.New("db down")}
	w := &syncWorker{svc: syncer, notify: notify, log: log}

	require.NoError(t, w.Work(t.Context(), syncJob(1, 3)))

	require.Contains(t, buf.String(), `"msg":"mark sync failed"`)
	require.Equal(t, 1, notify.syncOutcome)
	require.Equal(t, 1, notify.reconnect)
}
