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
	res    docsbot.BackupResult
	err    error
	marked bool
	reason docsbot.BackupReason
}

func (b *revokedBackuper) Backup(context.Context, docsbot.UserID, docsbot.DocumentID) (docsbot.BackupResult, error) {
	return b.res, b.err
}

func (b *revokedBackuper) MarkBackupFailed(_ context.Context, _ docsbot.UserID, _ docsbot.DocumentID, reason docsbot.BackupReason) error {
	b.marked, b.reason = true, reason
	return nil
}

// spyReconnect records which notices a worker sent. It implements the notifier
// ports of all workers; outcomeIDs keeps the documents of UploadOutcome in order.
type spyReconnect struct {
	outcome, reconnect, syncOutcome, kept int
	outcomeIDs                            []docsbot.DocumentID
}

func (s *spyReconnect) UploadOutcome(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	s.outcome++
	s.outcomeIDs = append(s.outcomeIDs, id)
	return nil
}

func (s *spyReconnect) Reconnect(context.Context, docsbot.UserID) error {
	s.reconnect++
	return nil
}

func (s *spyReconnect) SyncOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	s.syncOutcome++
	return nil
}

func (s *spyReconnect) RemoteKept(context.Context, docsbot.UserID) error {
	s.kept++
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

func (failingNotifier) SyncOutcome(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return errors.New("telegram is down")
}

func (failingNotifier) RemoteKept(context.Context, docsbot.UserID) error {
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

// debugJSONLog returns a logger that writes every level as JSON into the
// returned buffer. Work is called from the test goroutine, so no lock is needed.
func debugJSONLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

// requireNoLeaks fails when the logs carry anything that must stay out of them:
// document titles, tokens and Telegram identifiers (CLAUDE.md, hard rule 7).
func requireNoLeaks(t *testing.T, out string) {
	t.Helper()
	for _, leak := range []string{"ПаспортSECRET", "SECRET", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}

// secretDoc is a document whose title and file id must never reach the logs, so
// requireNoLeaks proves something.
var secretDoc = docsbot.Document{
	ID: "d1", UserID: "u1", Title: "ПаспортSECRET.pdf", TgFileID: "123:ABC",
	Category: docsbot.CatPassport, BackupStatus: docsbot.BackupBackedUp,
}

func uploadJob(attempt, maxAttempts int) *river.Job[UploadDocumentArgs] {
	return &river.Job[UploadDocumentArgs]{
		JobRow: &rivertype.JobRow{ID: 7, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   UploadDocumentArgs{UserID: "u1", DocumentID: "d1"},
	}
}

// The worker logs each stage with ids and timings, never the document itself.
func TestUploadWorker_logsLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		svc       *revokedBackuper
		wantErr   bool
		wantLevel string
		wantMsg   string
		wantAttrs []string
	}{
		{
			name:      "success",
			svc:       &revokedBackuper{res: docsbot.BackupResult{Doc: secretDoc}},
			wantLevel: "INFO",
			wantMsg:   "upload finished",
			wantAttrs: []string{`"backup_status":"backed_up"`, `"category":"passport"`, `"duration_ms"`},
		},
		{
			name:      "retry",
			svc:       &revokedBackuper{err: errors.New("telegram down")},
			wantErr:   true,
			wantLevel: "DEBUG",
			wantMsg:   "upload retry",
			wantAttrs: []string{`"attempt":1`},
		},
		{
			name:      "revoke",
			svc:       &revokedBackuper{err: docsbot.ErrDriveRevoked},
			wantLevel: "WARN",
			wantMsg:   "drive upload: access revoked",
			wantAttrs: []string{`"duration_ms"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			w := &uploadWorker{svc: tt.svc, notify: &spyReconnect{}, log: log}

			err := w.Work(t.Context(), uploadJob(1, 3))

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			out := buf.String()
			require.Contains(t, out, `"msg":"upload started"`)
			require.Contains(t, out, `"level":"`+tt.wantLevel+`","msg":"`+tt.wantMsg+`"`)
			for _, attr := range append(tt.wantAttrs, `"doc_id":"d1"`, `"user_id":"u1"`, `"job_id":7`) {
				require.Contains(t, out, attr)
			}
			requireNoLeaks(t, out)
		})
	}
}

// Results that need no user-facing notice or carry a side warning.
func TestUploadWorker_resultBranches(t *testing.T) {
	tests := []struct {
		name        string
		res         docsbot.BackupResult
		wantOutcome int
		wantLog     string
		notWantLog  string
	}{
		{"nothing to do stays silent", docsbot.BackupResult{Noop: true}, 0, `"msg":"upload noop"`, `"msg":"upload finished"`},
		{"too large still updates the status line", docsbot.BackupResult{Doc: docsbot.Document{BackupStatus: docsbot.BackupTooLarge}}, 1, `"backup_status":"too_large"`, ""},
		{"orphan trash failure is logged and the upload still ends", docsbot.BackupResult{Doc: secretDoc, OrphanTrashErr: errors.New("drive 500")}, 1, `"msg":"trash orphan drive file failed"`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			notify := &spyReconnect{}
			w := &uploadWorker{svc: &revokedBackuper{res: tt.res}, notify: notify, log: log}

			require.NoError(t, w.Work(t.Context(), uploadJob(1, 3)))

			require.Equal(t, tt.wantOutcome, notify.outcome)
			out := buf.String()
			require.Contains(t, out, tt.wantLog)
			if tt.notWantLog != "" {
				require.NotContains(t, out, tt.notWantLog)
			}
			requireNoLeaks(t, out)
		})
	}
}
