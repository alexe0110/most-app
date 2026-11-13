package jobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type revokedBackuper struct {
	err    error
	marked bool
	reason docsbot.BackupReason
}

func (b *revokedBackuper) Backup(context.Context, docsbot.UserID, docsbot.DocumentID) (docsbot.BackupResult, error) {
	return docsbot.BackupResult{}, b.err
}

func (b *revokedBackuper) MarkBackupFailed(_ context.Context, _ docsbot.UserID, _ docsbot.DocumentID, reason docsbot.BackupReason) error {
	b.marked, b.reason = true, reason
	return nil
}

type spyReconnect struct{ outcome, reconnect int }

func (s *spyReconnect) UploadOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	s.outcome++
	return nil
}

func (s *spyReconnect) Reconnect(context.Context, docsbot.UserID) error {
	s.reconnect++
	return nil
}

// A revoked Drive grant (the mapped error of driveBridge.Upload) is terminal:
// the document is marked failed, the user is told, asked to reconnect, and the
// job is not retried. Detecting a file deleted or renamed in Drive needs a new
// Drive call and is deferred.
func TestUploadWorker_revokedAsksToReconnect(t *testing.T) {
	svc := &revokedBackuper{err: docsbot.ErrDriveRevoked}
	notify := &spyReconnect{}
	w := &uploadWorker{svc: svc, notify: notify, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	job := &river.Job[UploadDocumentArgs]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, MaxAttempts: 3},
		Args:   UploadDocumentArgs{UserID: "u", DocumentID: "d"},
	}

	require.NoError(t, w.Work(t.Context(), job), "revoked is terminal, River must not retry")
	require.True(t, svc.marked)
	require.Equal(t, docsbot.BackupReasonRevoked, svc.reason)
	require.Equal(t, 1, notify.outcome)
	require.Equal(t, 1, notify.reconnect)
}

// A failure is classified into the stored reason: a full Drive is terminal at
// once (no reconnect prompt), the last attempt of any other error is
// "unavailable", and an earlier attempt is left to River.
func TestUploadWorker_failureReasons(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		attempt    int
		wantReason docsbot.BackupReason
		wantRetry  bool
	}{
		{"drive full is terminal", docsbot.ErrDriveFull, 1, docsbot.BackupReasonQuota, false},
		{"last attempt is unavailable", errors.New("boom"), 3, docsbot.BackupReasonUnavailable, true},
		{"early attempt is retried unmarked", errors.New("boom"), 1, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &revokedBackuper{err: tt.err}
			notify := &spyReconnect{}
			w := &uploadWorker{svc: svc, notify: notify, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			job := &river.Job[UploadDocumentArgs]{
				JobRow: &rivertype.JobRow{ID: 1, Attempt: tt.attempt, MaxAttempts: 3},
				Args:   UploadDocumentArgs{UserID: "u", DocumentID: "d"},
			}

			err := w.Work(t.Context(), job)
			if tt.wantRetry {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantReason, svc.reason)
			require.Zero(t, notify.reconnect, "no reconnect prompt")
		})
	}
}

type failingNotifier struct{}

func (failingNotifier) UploadOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return errors.New("telegram is down")
}

func (failingNotifier) Reconnect(context.Context, docsbot.UserID) error {
	return errors.New("telegram is down")
}

// A failed notice is logged with ids only and never fails the job: a retry
// would repeat the upload for a message that is best-effort anyway.
func TestUploadWorker_notifyErrorIsLoggedNotReturned(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	w := &uploadWorker{svc: &revokedBackuper{err: docsbot.ErrDriveRevoked}, notify: failingNotifier{}, log: log}
	job := &river.Job[UploadDocumentArgs]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, MaxAttempts: 3},
		Args:   UploadDocumentArgs{UserID: "u1", DocumentID: "d1"},
	}

	require.NoError(t, w.Work(t.Context(), job))

	out := logs.String()
	require.Contains(t, out, "notify user failed")
	require.Contains(t, out, "notice=UploadOutcome")
	require.Contains(t, out, "notice=Reconnect")
	require.Contains(t, out, "user_id=u1")
	require.Contains(t, out, "doc_id=d1")
}
