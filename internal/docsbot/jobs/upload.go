// Package jobs holds the Most Docs background workers (River).
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
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

// UploadQueue is the River queue for Drive uploads. It runs one worker so
// parallel uploads cannot race on creating the same Drive folders.
const UploadQueue = "drive_upload"

// UploadDocumentArgs is the River job that backs a saved document up to Drive.
type UploadDocumentArgs struct {
	UserID     string
	DocumentID string
}

// Kind implements river.JobArgs.
func (UploadDocumentArgs) Kind() string { return "upload_document" }

// Backuper is the subset of docsbot.Service the worker needs.
type Backuper interface {
	Backup(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.BackupResult, error)
	MarkBackupFailed(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error
}

// Notifier tells the user in Telegram how an upload ended. Implemented by the
// telegram adapter; errors are best-effort.
type Notifier interface {
	// UploadOutcome reports a document's final state (backed up, failed or too large).
	UploadOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error
	Reconnect(ctx context.Context, user docsbot.UserID) error
}

type uploadWorker struct {
	river.WorkerDefaults[UploadDocumentArgs]
	svc    Backuper
	notify Notifier
	log    *slog.Logger
}

// Work uploads one document. A revoked grant is terminal until the user
// reconnects; other errors are retried by River until MaxAttempts.
func (w *uploadWorker) Work(ctx context.Context, job *river.Job[UploadDocumentArgs]) error {
	user := docsbot.UserID(job.Args.UserID)
	id := docsbot.DocumentID(job.Args.DocumentID)

	start := time.Now()
	w.log.InfoContext(ctx, "upload started", "job_id", job.ID, "attempt", job.Attempt, "user_id", user, "doc_id", id)

	res, err := w.svc.Backup(ctx, user, id)
	switch {
	case errors.Is(err, docsbot.ErrDriveRevoked):
		if err := w.svc.MarkBackupFailed(ctx, user, id); err != nil {
			w.log.ErrorContext(ctx, "mark backup failed", "err", err, "user_id", user, "doc_id", id)
		}
		_ = w.notify.UploadOutcome(ctx, user, id)
		_ = w.notify.Reconnect(ctx, user)
		w.log.WarnContext(ctx, "drive upload: access revoked", "job_id", job.ID, "attempt", job.Attempt,
			"user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
		return nil
	case err != nil:
		if job.Attempt >= job.MaxAttempts {
			if markErr := w.svc.MarkBackupFailed(ctx, user, id); markErr != nil {
				w.log.ErrorContext(ctx, "mark backup failed", "err", markErr, "user_id", user, "doc_id", id)
			}
			_ = w.notify.UploadOutcome(ctx, user, id)
			w.log.ErrorContext(ctx, "drive upload failed permanently", "job_id", job.ID, "attempt", job.Attempt,
				"err", err, "user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
		} else {
			w.log.DebugContext(ctx, "upload retry", "attempt", job.Attempt, "err", err, "user_id", user, "doc_id", id)
		}
		return fmt.Errorf("upload document: %w", err)
	}
	if res.Noop {
		w.log.DebugContext(ctx, "upload noop", "job_id", job.ID, "attempt", job.Attempt, "user_id", user, "doc_id", id)
		return nil
	}
	w.log.InfoContext(ctx, "upload finished", "user_id", user, "doc_id", id, "category", res.Doc.Category,
		"backup_status", res.Doc.BackupStatus, "duration_ms", time.Since(start).Milliseconds())
	// A too-large file stays in Telegram (backup_status=too_large); the outcome
	// still updates its status line.
	_ = w.notify.UploadOutcome(ctx, user, id)
	return nil
}

// RegisterUploadDocument registers the worker and returns a binder that injects
// its dependencies. Call the binder before queue.Start: Work runs only after.
func RegisterUploadDocument(workers *river.Workers, log *slog.Logger) func(Backuper, Notifier) {
	w := &uploadWorker{log: log}
	river.AddWorker(workers, w)
	return func(svc Backuper, n Notifier) { w.svc, w.notify = svc, n }
}

// Enqueuer implements docsbot.Uploads atomically over the repo and the queue.
type Enqueuer struct {
	repo  *postgres.Repo
	queue *pjobs.Client
}

var _ docsbot.Uploads = (*Enqueuer)(nil)

// NewEnqueuer wires an Enqueuer around the repo and the job queue.
func NewEnqueuer(repo *postgres.Repo, queue *pjobs.Client) *Enqueuer {
	return &Enqueuer{repo: repo, queue: queue}
}

// uploadInsertOpts makes an upload job unique by args among live states, so a
// repeated insert while an upload is queued or running is skipped. Finished
// states are excluded so a retry or a post-reconnect upload still runs.
func uploadInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue: UploadQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// EnqueueUpload marks the document pending and enqueues its upload in one tx.
// The job is unique by args among live states, so a repeated call while an
// upload is queued or running is skipped and leaves the document status as is.
// Finished states are excluded so a retry or a post-reconnect upload still runs.
func (e *Enqueuer) EnqueueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return e.repo.EnqueueUpload(ctx, id, func(ctx context.Context, tx pgx.Tx) (bool, error) {
		res, err := e.queue.InsertTx(ctx, tx, UploadDocumentArgs{UserID: string(user), DocumentID: string(id)}, uploadInsertOpts())
		if err != nil {
			return false, err
		}
		return res.UniqueSkippedAsDuplicate, nil
	})
}

// RequeueUpload inserts the upload job only, with the same uniqueness as
// EnqueueUpload and without touching the document, so a live job dedups to a
// no-op and the stuck-since clock (updated_at) is preserved.
func (e *Enqueuer) RequeueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	_, err := e.queue.Insert(ctx, UploadDocumentArgs{UserID: string(user), DocumentID: string(id)}, uploadInsertOpts())
	if err != nil {
		return fmt.Errorf("requeue upload: %w", err)
	}
	return nil
}

// CancelUploads removes the user's pending (not yet running) upload_document jobs.
func (e *Enqueuer) CancelUploads(ctx context.Context, user docsbot.UserID) error {
	if _, err := e.queue.DeleteJobsByArg(ctx, UploadDocumentArgs{}.Kind(), "UserID", string(user)); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	return nil
}
