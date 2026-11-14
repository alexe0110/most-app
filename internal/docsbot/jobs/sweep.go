package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// sweepInterval is how often the sweeper looks for stuck uploads.
const sweepInterval = 10 * time.Minute

// SweepUploadsArgs is the periodic job that recovers uploads stuck in
// pending/uploading. It carries no payload.
type SweepUploadsArgs struct{}

// Kind implements river.JobArgs.
func (SweepUploadsArgs) Kind() string { return "sweep_uploads" }

// Sweeper is the subset of docsbot.Service the sweep worker needs.
type Sweeper interface {
	SweepStuckUploads(ctx context.Context) (docsbot.SweepResult, error)
}

type sweepWorker struct {
	river.WorkerDefaults[SweepUploadsArgs]
	svc    Sweeper
	notify Notifier
	log    *slog.Logger
}

// Work runs one sweep and tells the users whose uploads were failed.
func (w *sweepWorker) Work(ctx context.Context, _ *river.Job[SweepUploadsArgs]) error {
	res, err := w.svc.SweepStuckUploads(ctx)
	if err != nil {
		w.log.ErrorContext(ctx, "sweep uploads failed", "err", err,
			"found", res.Found, "requeued", res.Requeued, "failed", len(res.Failed))
		return fmt.Errorf("sweep uploads: %w", err)
	}
	for _, f := range res.Failed {
		notifyWarn(ctx, w.log, "UploadOutcome", w.notify.UploadOutcome(ctx, f.User, f.ID), f.User, f.ID)
	}
	w.log.InfoContext(ctx, "sweep uploads finished",
		"found", res.Found, "requeued", res.Requeued, "failed", len(res.Failed),
		"syncs_requeued", res.SyncsRequeued, "syncs_failed", res.SyncsFailed)
	return nil
}

// RegisterSweepUploads registers the sweep worker and returns its periodic job.
func RegisterSweepUploads(workers *river.Workers, log *slog.Logger, svc Sweeper, notify Notifier) *river.PeriodicJob {
	river.AddWorker(workers, &sweepWorker{svc: svc, notify: notify, log: log})
	return river.NewPeriodicJob(
		river.PeriodicInterval(sweepInterval),
		func() (river.JobArgs, *river.InsertOpts) { return SweepUploadsArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
