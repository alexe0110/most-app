package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type revokedBackuper struct{ marked bool }

func (*revokedBackuper) Backup(context.Context, docsbot.UserID, docsbot.DocumentID) (docsbot.BackupResult, error) {
	return docsbot.BackupResult{}, docsbot.ErrDriveRevoked
}

func (b *revokedBackuper) MarkBackupFailed(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	b.marked = true
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
// Drive call and is deferred to #11.
func TestUploadWorker_revokedAsksToReconnect(t *testing.T) {
	svc := &revokedBackuper{}
	notify := &spyReconnect{}
	w := &uploadWorker{svc: svc, notify: notify, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	job := &river.Job[UploadDocumentArgs]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: 1, MaxAttempts: 3},
		Args:   UploadDocumentArgs{UserID: "u", DocumentID: "d"},
	}

	require.NoError(t, w.Work(t.Context(), job), "revoked is terminal, River must not retry")
	require.True(t, svc.marked)
	require.Equal(t, 1, notify.outcome)
	require.Equal(t, 1, notify.reconnect)
}
