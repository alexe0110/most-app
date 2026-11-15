package jobs

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type fakePurger struct {
	res docsbot.PurgeResult
	err error
}

func (p fakePurger) PurgeStalePending(context.Context) (docsbot.PurgeResult, error) {
	return p.res, p.err
}

func TestPurgePendingWorker_work(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name      string
		purger    fakePurger
		wantErr   error
		wantLevel string
		wantMsg   string
	}{
		{
			name:      "logs what was deleted",
			purger:    fakePurger{res: docsbot.PurgeResult{Pending: 3, StatusMessages: 2}},
			wantLevel: "INFO", wantMsg: "purge pending finished",
		},
		{
			// The service keeps what it already deleted, so the failure line
			// still reports the partial counts.
			name:      "failure is returned wrapped and logs partial counts",
			purger:    fakePurger{res: docsbot.PurgeResult{Pending: 3, StatusMessages: 2}, err: boom},
			wantErr:   boom,
			wantLevel: "ERROR", wantMsg: "purge pending failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, buf := debugJSONLog()
			w := &purgePendingWorker{svc: tt.purger, log: log}
			job := &river.Job[PurgePendingArgs]{JobRow: &rivertype.JobRow{ID: 7, Attempt: 1, MaxAttempts: 3}}

			err := w.Work(t.Context(), job)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, "purge pending")
			} else {
				require.NoError(t, err)
			}
			out := buf.String()
			require.Contains(t, out, `"level":"`+tt.wantLevel+`","msg":"`+tt.wantMsg+`"`)
			require.Contains(t, out, `"deleted":3`)
			require.Contains(t, out, `"status_messages_deleted":2`)
		})
	}
}

func TestRegisterPurgePending_returnsPeriodicJob(t *testing.T) {
	workers := river.NewWorkers()

	periodic := RegisterPurgePending(workers, slog.New(slog.DiscardHandler), fakePurger{})

	require.NotNil(t, periodic)
}
