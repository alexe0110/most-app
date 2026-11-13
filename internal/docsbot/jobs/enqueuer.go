package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/alexe0110/most-app/internal/docsbot"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

// enqueueStore is the part of the repo Enqueuer needs: every method changes a
// document and runs insert (the job insert) in the same tx.
type enqueueStore interface {
	EnqueueUpload(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) (skip bool, err error)) error
	SetTitleForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error)
	SetCategoryForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error)
	MarkBackedUpForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error)
	ReplaceContentForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error)
	EnqueueSyncJob(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) error) error
	RetrySyncJob(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, insert func(ctx context.Context, tx pgx.Tx) error) (bool, error)
	DeleteForSync(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, queueTrash bool, insert func(ctx context.Context, tx pgx.Tx, remoteID string) error) (bool, error)
}

// Enqueuer implements docsbot.Uploads and docsbot.Syncs atomically over the
// store and the queue.
type Enqueuer struct {
	store enqueueStore
	queue *pjobs.Client
}

var (
	_ docsbot.Uploads = (*Enqueuer)(nil)
	_ docsbot.Syncs   = (*Enqueuer)(nil)
)

// NewEnqueuer wires an Enqueuer around the store and the job queue.
func NewEnqueuer(store enqueueStore, queue *pjobs.Client) *Enqueuer {
	return &Enqueuer{store: store, queue: queue}
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
	return e.store.EnqueueUpload(ctx, user, id, func(ctx context.Context, tx pgx.Tx) (bool, error) {
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

// CancelUploads removes the user's pending (not yet running) upload_document
// jobs. Sync and trash jobs stay: their workers handle a disconnected Drive.
func (e *Enqueuer) CancelUploads(ctx context.Context, user docsbot.UserID) error {
	return e.cancel(ctx, user, UploadDocumentArgs{}.Kind())
}

// CancelUserJobs removes the user's pending upload_document, storage_sync and
// storage_trash jobs.
func (e *Enqueuer) CancelUserJobs(ctx context.Context, user docsbot.UserID) error {
	return e.cancel(ctx, user, UploadDocumentArgs{}.Kind(), StorageSyncArgs{}.Kind(), StorageTrashArgs{}.Kind())
}

func (e *Enqueuer) cancel(ctx context.Context, user docsbot.UserID, kinds ...string) error {
	for _, kind := range kinds {
		if _, err := e.queue.DeleteJobsByArg(ctx, kind, "UserID", string(user)); err != nil {
			return fmt.Errorf("cancel jobs: %w", err)
		}
	}
	return nil
}
