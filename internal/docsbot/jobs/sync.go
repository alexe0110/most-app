package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

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
// Implemented by the telegram adapter; errors are best-effort.
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
		_ = w.notify.SyncOutcome(ctx, user, id)
		_ = w.notify.Reconnect(ctx, user)
		w.log.WarnContext(ctx, "drive sync: access revoked", "job_id", job.ID, "attempt", job.Attempt,
			"user_id", user, "doc_id", id)
		return nil
	case err != nil:
		if job.Attempt >= job.MaxAttempts {
			if markErr := w.svc.MarkSyncFailed(ctx, user, id, ""); markErr != nil {
				w.log.ErrorContext(ctx, "mark sync failed", "err", markErr, "user_id", user, "doc_id", id)
			}
			_ = w.notify.SyncOutcome(ctx, user, id)
			w.log.ErrorContext(ctx, "drive sync failed permanently", "job_id", job.ID, "attempt", job.Attempt,
				"err", err, "user_id", user, "doc_id", id)
		} else {
			w.log.DebugContext(ctx, "sync retry", "attempt", job.Attempt, "err", err, "user_id", user, "doc_id", id)
		}
		return fmt.Errorf("sync document: %w", err)
	}
	switch {
	case res.Missing:
		_ = w.notify.SyncOutcome(ctx, user, id)
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

// syncInsertOpts makes a sync job unique by args among queued states only. A
// running job is excluded on purpose: a change made while it runs must queue a
// follow-up, because the running job may already have read the old state.
func syncInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue: UploadQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateScheduled,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// insertSync enqueues a sync job inside tx; a duplicate of a queued one is fine.
func (e *Enqueuer) insertSync(user docsbot.UserID, id docsbot.DocumentID) func(ctx context.Context, tx pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := e.queue.InsertTx(ctx, tx, StorageSyncArgs{UserID: string(user), DocumentID: string(id)}, syncInsertOpts())
		if err != nil {
			return fmt.Errorf("insert sync job: %w", err)
		}
		return nil
	}
}

// SetTitle implements docsbot.Syncs.
func (e *Enqueuer) SetTitle(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) (bool, error) {
	return e.store.SetTitleForSync(ctx, user, id, title, e.insertSync(user, id))
}

// SetCategory implements docsbot.Syncs.
func (e *Enqueuer) SetCategory(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (bool, error) {
	return e.store.SetCategoryForSync(ctx, user, id, cat, e.insertSync(user, id))
}

// EnqueueSync implements docsbot.Syncs.
func (e *Enqueuer) EnqueueSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return e.store.EnqueueSyncJob(ctx, user, id, e.insertSync(user, id))
}

// RetrySync implements docsbot.Syncs.
func (e *Enqueuer) RetrySync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	return e.store.RetrySyncJob(ctx, user, id, e.insertSync(user, id))
}
