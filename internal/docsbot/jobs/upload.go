// Package jobs holds the Most Docs background workers (River).
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

// UploadQueue is the River queue for Drive uploads. It runs one worker so
// parallel uploads cannot race on creating the same Drive folders. Sync
// correctness does not depend on it: meta_version guards stale syncs.
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
	MarkBackupFailed(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, reason docsbot.BackupReason) error
}

// Notifier tells the user in Telegram how an upload ended. Implemented by the
// telegram adapter, which only returns errors; the worker logs them (see
// notifyWarn) and never fails the job over a notice.
type Notifier interface {
	// UploadOutcome reports a document's final state (backed up, failed or too large).
	UploadOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error
	Reconnect(ctx context.Context, user docsbot.UserID) error
}

// notifyWarn logs a failed user notification at Warn. A notice is best-effort:
// failing the job would retry the upload and could repeat the notice. The log
// carries ids only, never titles or file ids.
func notifyWarn(ctx context.Context, log *slog.Logger, notice string, err error, user docsbot.UserID, doc ...docsbot.DocumentID) {
	if err == nil {
		return
	}
	attrs := []any{"notice", notice, "err", err, "user_id", user}
	if len(doc) > 0 {
		attrs = append(attrs, "doc_id", doc[0])
	}
	log.WarnContext(ctx, "notify user failed", attrs...)
}

type uploadWorker struct {
	river.WorkerDefaults[UploadDocumentArgs]
	svc    Backuper
	notify Notifier
	log    *slog.Logger
}

// Work uploads one document. A revoked grant is terminal until the user
// reconnects, a full Drive until the user frees space; other errors are retried by River until MaxAttempts.
func (w *uploadWorker) Work(ctx context.Context, job *river.Job[UploadDocumentArgs]) error {
	user := docsbot.UserID(job.Args.UserID)
	id := docsbot.DocumentID(job.Args.DocumentID)

	start := time.Now()
	w.log.InfoContext(ctx, "upload started", "job_id", job.ID, "attempt", job.Attempt, "user_id", user, "doc_id", id)

	res, err := w.svc.Backup(ctx, user, id)
	switch {
	case errors.Is(err, docsbot.ErrDriveRevoked):
		if err := w.svc.MarkBackupFailed(ctx, user, id, docsbot.BackupReasonRevoked); err != nil {
			w.log.ErrorContext(ctx, "mark backup failed", "err", err, "user_id", user, "doc_id", id)
		}
		notifyWarn(ctx, w.log, "UploadOutcome", w.notify.UploadOutcome(ctx, user, id), user, id)
		notifyWarn(ctx, w.log, "Reconnect", w.notify.Reconnect(ctx, user), user)
		w.log.WarnContext(ctx, "drive upload: access revoked", "job_id", job.ID, "attempt", job.Attempt,
			"user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
		return nil
	case errors.Is(err, docsbot.ErrDriveFull):
		// Terminal at once, like a revoked grant, but there is nothing to reconnect.
		if err := w.svc.MarkBackupFailed(ctx, user, id, docsbot.BackupReasonQuota); err != nil {
			w.log.ErrorContext(ctx, "mark backup failed", "err", err, "user_id", user, "doc_id", id)
		}
		notifyWarn(ctx, w.log, "UploadOutcome", w.notify.UploadOutcome(ctx, user, id), user, id)
		w.log.WarnContext(ctx, "drive upload: storage full", "job_id", job.ID, "attempt", job.Attempt,
			"user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
		return nil
	case err != nil:
		if job.Attempt >= job.MaxAttempts {
			if markErr := w.svc.MarkBackupFailed(ctx, user, id, docsbot.BackupReasonUnavailable); markErr != nil {
				w.log.ErrorContext(ctx, "mark backup failed", "err", markErr, "user_id", user, "doc_id", id)
			}
			notifyWarn(ctx, w.log, "UploadOutcome", w.notify.UploadOutcome(ctx, user, id), user, id)
			w.log.ErrorContext(ctx, "drive upload failed permanently", "job_id", job.ID, "attempt", job.Attempt,
				"err", err, "user_id", user, "doc_id", id, "duration_ms", time.Since(start).Milliseconds())
		} else {
			w.log.DebugContext(ctx, "upload retry", "attempt", job.Attempt, "err", err, "user_id", user, "doc_id", id)
		}
		return fmt.Errorf("upload document: %w", err)
	}
	if res.OrphanTrashErr != nil {
		w.log.ErrorContext(ctx, "trash orphan drive file failed", "err", res.OrphanTrashErr, "job_id", job.ID, "user_id", user, "doc_id", id)
	}
	if res.Noop {
		w.log.DebugContext(ctx, "upload noop", "job_id", job.ID, "attempt", job.Attempt, "user_id", user, "doc_id", id)
		return nil
	}
	w.log.InfoContext(ctx, "upload finished", "user_id", user, "doc_id", id, "category", res.Doc.Category,
		"backup_status", res.Doc.BackupStatus, "duration_ms", time.Since(start).Milliseconds())
	// A too-large file stays in Telegram (backup_status=too_large); the outcome
	// still updates its status line.
	notifyWarn(ctx, w.log, "UploadOutcome", w.notify.UploadOutcome(ctx, user, id), user, id)
	return nil
}

// RegisterUploadDocument registers the worker and returns a binder that injects
// its dependencies. Call the binder before queue.Start: Work runs only after.
func RegisterUploadDocument(workers *river.Workers, log *slog.Logger) func(Backuper, Notifier) {
	w := &uploadWorker{log: log}
	river.AddWorker(workers, w)
	return func(svc Backuper, n Notifier) { w.svc, w.notify = svc, n }
}
