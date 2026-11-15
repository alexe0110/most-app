package jobs

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type fakeSweeper struct {
	res docsbot.SweepResult
	err error
}

func (s fakeSweeper) SweepStuckUploads(context.Context) (docsbot.SweepResult, error) {
	return s.res, s.err
}

func sweepJob() *river.Job[SweepUploadsArgs] {
	return &river.Job[SweepUploadsArgs]{JobRow: &rivertype.JobRow{ID: 7, Attempt: 1, MaxAttempts: 3}}
}

func TestSweepWorker_errorIsReturnedWrappedAndLoggedWithCounts(t *testing.T) {
	boom := errors.New("db down")
	log, buf := debugJSONLog()
	notify := &spyReconnect{}
	res := docsbot.SweepResult{Found: 5, Requeued: 2, Failed: []docsbot.StuckUpload{{User: "u1", ID: "d1"}}}
	w := &sweepWorker{svc: fakeSweeper{res: res, err: boom}, notify: notify, log: log}

	err := w.Work(t.Context(), sweepJob())

	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "sweep uploads")
	require.Zero(t, notify.outcome, "a failed sweep tells nobody")
	out := buf.String()
	require.Contains(t, out, `"level":"ERROR","msg":"sweep uploads failed"`)
	require.Contains(t, out, `"found":5`)
	require.Contains(t, out, `"requeued":2`)
	require.Contains(t, out, `"failed":1`)
}

func TestSweepWorker_notifiesEveryFailedUpload(t *testing.T) {
	log, buf := debugJSONLog()
	notify := &spyReconnect{}
	res := docsbot.SweepResult{
		Found: 3, Requeued: 1, SyncsRequeued: 4, SyncsFailed: 2,
		Failed: []docsbot.StuckUpload{{User: "u1", ID: "d1"}, {User: "u2", ID: "d2"}},
	}
	w := &sweepWorker{svc: fakeSweeper{res: res}, notify: notify, log: log}

	require.NoError(t, w.Work(t.Context(), sweepJob()))

	require.Equal(t, []docsbot.DocumentID{"d1", "d2"}, notify.outcomeIDs)
	require.Zero(t, notify.reconnect, "a stuck upload does not ask to reconnect")
	out := buf.String()
	require.Contains(t, out, `"level":"INFO","msg":"sweep uploads finished"`)
	for _, attr := range []string{`"found":3`, `"requeued":1`, `"failed":2`, `"syncs_requeued":4`, `"syncs_failed":2`} {
		require.Contains(t, out, attr)
	}
	requireNoLeaks(t, out)
}

func TestSweepWorker_nothingStuckStaysSilent(t *testing.T) {
	log, buf := debugJSONLog()
	notify := &spyReconnect{}
	w := &sweepWorker{svc: fakeSweeper{}, notify: notify, log: log}

	require.NoError(t, w.Work(t.Context(), sweepJob()))

	require.Zero(t, notify.outcome)
	require.Contains(t, buf.String(), `"msg":"sweep uploads finished"`)
}

// A failed notice is logged and the next user is still told; failing the job
// would re-run a sweep that already changed the rows.
func TestSweepWorker_notifyErrorIsLoggedNotReturned(t *testing.T) {
	log, buf := debugJSONLog()
	res := docsbot.SweepResult{Failed: []docsbot.StuckUpload{{User: "u1", ID: "d1"}, {User: "u2", ID: "d2"}}}
	w := &sweepWorker{svc: fakeSweeper{res: res}, notify: failingNotifier{}, log: log}

	require.NoError(t, w.Work(t.Context(), sweepJob()))

	out := buf.String()
	require.Equal(t, 2, strings.Count(out, `"msg":"notify user failed"`), "both users were tried")
	require.Contains(t, out, `"notice":"UploadOutcome"`)
	require.Contains(t, out, `"doc_id":"d2"`)
	require.Contains(t, out, `"msg":"sweep uploads finished"`)
}

// The sweep runs on start and then on a timer; without the periodic job nothing
// would ever recover a stuck upload.
func TestRegisterSweepUploads_returnsPeriodicJob(t *testing.T) {
	workers := river.NewWorkers()

	periodic := RegisterSweepUploads(workers, slog.New(slog.DiscardHandler), fakeSweeper{}, &spyReconnect{})

	require.NotNil(t, periodic)
}
