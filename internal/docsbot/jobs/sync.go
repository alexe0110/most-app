package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// StorageSyncArgs is the River job that makes a document's cloud file match its
// row (name, category folder, content). It carries ids only: the worker reads
// the desired state from the database, so any number of changes collapse into
// one run.
type StorageSyncArgs struct {
	UserID     string
	DocumentID string
}

// Kind implements river.JobArgs.
func (StorageSyncArgs) Kind() string { return "storage_sync" }

// Syncer is the subset of docsbot.Service the sync worker needs.
type Syncer interface {
	SyncRemote(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.SyncResult, error)
	MarkSyncFailed(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, reason string) error
}

// SyncNotifier tells the user in Telegram when a sync needs attention.
// Implemented by the telegram adapter; errors are returned, not logged: the worker logs them.
type SyncNotifier interface {
	// SyncOutcome shows the document's card after a failed or missing sync.
	SyncOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error
	Reconnect(ctx context.Context, user docsbot.UserID) error
}

type syncWorker struct {
	river.WorkerDefaults[StorageSyncArgs]
	svc    Syncer
	notify SyncNotifier
	log    *slog.Logger
}

// Work syncs one document. A revoked grant is terminal until the user
// reconnects; other errors are retried by River until MaxAttempts.
func (w *syncWorker) Work(ctx context.Context, job *river.Job[StorageSyncArgs]) error {
	user := docsbot.UserID(job.Args.UserID)
	id := docsbot.DocumentID(job.Args.DocumentID)

	start := time.Now()
	res, err := w.svc.SyncRemote(ctx, user, id)
	switch {
	case errors.Is(err, docsbot.ErrDriveRevoked):
		if err := w.svc.MarkSyncFailed(ctx, user, id, docsbot.SyncReasonRevoked); err != nil {
			w.log.ErrorContext(ctx, "mark sync failed", "err", err, "user_id", user, "doc_id", id)
		}
		notifyWarn(ctx, w.log, "SyncOutcome", w.notify.SyncOutcome(ctx, user, id), user, id)
		notifyWarn(ctx, w.log, "Reconnect", w.notify.Reconnect(ctx, user), user)
		w.log.WarnContext(ctx, "drive sync: access revoked", "job_id", job.ID, "attempt", job.Attempt,
			"user_id", user, "doc_id", id)
		return nil
	case err != nil:
		if job.Attempt >= job.MaxAttempts {
			if markErr := w.svc.MarkSyncFailed(ctx, user, id, ""); markErr != nil {
				w.log.ErrorContext(ctx, "mark sync failed", "err", markErr, "user_id", user, "doc_id", id)
			}
			notifyWarn(ctx, w.log, "SyncOutcome", w.notify.SyncOutcome(ctx, user, id), user, id)
			w.log.ErrorContext(ctx, "drive sync failed permanently", "job_id", job.ID, "attempt", job.Attempt,
				"err", err, "user_id", user, "doc_id", id)
		} else {
			w.log.DebugContext(ctx, "sync retry", "attempt", job.Attempt, "err", err, "user_id", user, "doc_id", id)
		}
		return fmt.Errorf("sync document: %w", err)
	}
	switch {
	case res.Missing:
		notifyWarn(ctx, w.log, "SyncOutcome", w.notify.SyncOutcome(ctx, user, id), user, id)
		w.log.WarnContext(ctx, "drive sync: remote file missing", "user_id", user, "doc_id", id)
	case res.Noop:
		w.log.DebugContext(ctx, "sync noop", "job_id", job.ID, "user_id", user, "doc_id", id)
	default:
		w.log.InfoContext(ctx, "sync finished", "user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
	}
	return nil
}

// RegisterStorageSync registers the worker and returns a binder that injects
// its dependencies. Call the binder before queue.Start: Work runs only after.
func RegisterStorageSync(workers *river.Workers, log *slog.Logger) func(Syncer, SyncNotifier) {
	w := &syncWorker{log: log}
	river.AddWorker(workers, w)
	return func(svc Syncer, n SyncNotifier) { w.svc, w.notify = svc, n }
}

// syncInsertOpts leaves jobs non-unique on purpose. River's unique option must
// cover the running state, which would drop a follow-up queued while a sync runs
// (the running job may already have read the old state). The worker is
// idempotent and exits early when the document is in sync, so a burst of changes
// costs a few no-op jobs, not extra cloud calls.
func syncInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: UploadQueue}
}
