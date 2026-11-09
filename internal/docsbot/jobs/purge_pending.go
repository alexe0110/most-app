package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
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
	PurgeStalePending(ctx context.Context) (int, error)
}

type purgePendingWorker struct {
	river.WorkerDefaults[PurgePendingArgs]
	svc PendingPurger
	log *slog.Logger
}

// Work runs one purge.
func (w *purgePendingWorker) Work(ctx context.Context, _ *river.Job[PurgePendingArgs]) error {
	n, err := w.svc.PurgeStalePending(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "purge pending failed", "err", err)
		return fmt.Errorf("purge pending: %w", err)
	}
	w.log.InfoContext(ctx, "purge pending finished", "deleted", n)
	return nil
}

// RegisterPurgePending registers the worker and returns a binder that injects
// its dependency plus the periodic job. Call the binder before queue.Start.
func RegisterPurgePending(workers *river.Workers, log *slog.Logger) (bind func(PendingPurger), periodic *river.PeriodicJob) {
	w := &purgePendingWorker{log: log}
	river.AddWorker(workers, w)
	periodic = river.NewPeriodicJob(
		river.PeriodicInterval(purgePendingInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PurgePendingArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
	return func(svc PendingPurger) { w.svc = svc }, periodic
}
