package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// purgePendingInterval is how often abandoned pending documents are purged.
const purgePendingInterval = time.Hour

// PurgePendingArgs is the periodic job that deletes documents left pending
// (no category chosen) past their TTL. It carries no payload.
type PurgePendingArgs struct{}

// Kind implements river.JobArgs.
func (PurgePendingArgs) Kind() string { return "purge_pending" }

// PendingPurger is the subset of docsbot.Service the purge worker needs.
type PendingPurger interface {
	PurgeStalePending(ctx context.Context) (docsbot.PurgeResult, error)
}

type purgePendingWorker struct {
	river.WorkerDefaults[PurgePendingArgs]
	svc PendingPurger
	log *slog.Logger
}

// Work runs one purge.
func (w *purgePendingWorker) Work(ctx context.Context, _ *river.Job[PurgePendingArgs]) error {
	res, err := w.svc.PurgeStalePending(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "purge pending failed", "err", err, "deleted", res.Pending, "status_messages_deleted", res.StatusMessages)
		return fmt.Errorf("purge pending: %w", err)
	}
	w.log.InfoContext(ctx, "purge pending finished", "deleted", res.Pending, "status_messages_deleted", res.StatusMessages)
	return nil
}

// RegisterPurgePending registers the purge worker and returns its periodic job.
func RegisterPurgePending(workers *river.Workers, log *slog.Logger, svc PendingPurger) *river.PeriodicJob {
	river.AddWorker(workers, &purgePendingWorker{svc: svc, log: log})
	return river.NewPeriodicJob(
		river.PeriodicInterval(purgePendingInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PurgePendingArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
