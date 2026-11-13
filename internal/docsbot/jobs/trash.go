package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// providerGoogle names the cloud provider a trash job targets.
const providerGoogle = "google"

// StorageTrashArgs is the River job that moves a deleted document's cloud file
// to the provider's trash. The document row is already gone, so the job carries
// the remote id.
type StorageTrashArgs struct {
	UserID   string
	RemoteID string
	Provider string
}

// Kind implements river.JobArgs.
func (StorageTrashArgs) Kind() string { return "storage_trash" }

// Trasher is the subset of docsbot.Service the trash worker needs.
type Trasher interface {
	TrashRemote(ctx context.Context, user docsbot.UserID, remoteID string) error
}

// TrashNotifier tells the user the cloud file was left behind. Implemented by
// the telegram adapter; errors are returned, not logged: the worker logs them.
type TrashNotifier interface {
	RemoteKept(ctx context.Context, user docsbot.UserID) error
}

type trashWorker struct {
	river.WorkerDefaults[StorageTrashArgs]
	svc    Trasher
	notify TrashNotifier
	log    *slog.Logger
}

// Work trashes one remote file. A revoked or disconnected grant, or running out
// of attempts, leaves the file in the cloud and tells the user to remove it.
func (w *trashWorker) Work(ctx context.Context, job *river.Job[StorageTrashArgs]) error {
	user := docsbot.UserID(job.Args.UserID)

	err := w.svc.TrashRemote(ctx, user, job.Args.RemoteID)
	switch {
	case err == nil:
		w.log.InfoContext(ctx, "remote file trashed", "user_id", user, "provider", job.Args.Provider)
		return nil
	case errors.Is(err, docsbot.ErrDriveRevoked), errors.Is(err, docsbot.ErrDriveDisconnected):
		notifyWarn(ctx, w.log, "RemoteKept", w.notify.RemoteKept(ctx, user), user)
		w.log.WarnContext(ctx, "remote trash skipped: no drive access", "job_id", job.ID, "user_id", user, "provider", job.Args.Provider)
		return nil
	case job.Attempt >= job.MaxAttempts:
		notifyWarn(ctx, w.log, "RemoteKept", w.notify.RemoteKept(ctx, user), user)
		w.log.ErrorContext(ctx, "remote trash failed permanently", "job_id", job.ID, "attempt", job.Attempt,
			"err", err, "user_id", user, "provider", job.Args.Provider)
		return fmt.Errorf("trash remote: %w", err)
	}
	w.log.DebugContext(ctx, "trash retry", "attempt", job.Attempt, "err", err, "user_id", user)
	return fmt.Errorf("trash remote: %w", err)
}

// RegisterStorageTrash registers the worker and returns a binder that injects
// its dependencies. Call the binder before queue.Start: Work runs only after.
func RegisterStorageTrash(workers *river.Workers, log *slog.Logger) func(Trasher, TrashNotifier) {
	w := &trashWorker{log: log}
	river.AddWorker(workers, w)
	return func(svc Trasher, n TrashNotifier) { w.svc, w.notify = svc, n }
}

// DeleteDocument implements docsbot.Syncs: the row is deleted and, when the
// document had a live Drive file and trash is wanted, the trash job queued in
// one transaction.
func (e *Enqueuer) DeleteDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, trash bool) (bool, error) {
	return e.store.DeleteForSync(ctx, user, id, trash, func(ctx context.Context, tx pgx.Tx, remoteID string) error {
		args := StorageTrashArgs{UserID: string(user), RemoteID: remoteID, Provider: providerGoogle}
		if _, err := e.queue.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: UploadQueue}); err != nil {
			return fmt.Errorf("insert trash job: %w", err)
		}
		return nil
	})
}
