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

// providerGoogle names the cloud provider a trash job targets.
const providerGoogle = "google"

// Queue puts this bot's jobs on the River queue. Its Insert* methods run inside
// the caller's transaction (they satisfy postgres.JobInserter, checked where the
// two are wired in cmd/docsbot); the cancel methods implement docsbot.JobCanceler.
type Queue struct {
	client *pjobs.Client
}

var _ docsbot.JobCanceler = (*Queue)(nil)

// NewQueue wraps the shared job client.
func NewQueue(client *pjobs.Client) *Queue {
	return &Queue{client: client}
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

// InsertUpload enqueues the document's upload in tx. skipped reports that a live
// upload job already exists, so nothing was inserted.
func (q *Queue) InsertUpload(ctx context.Context, tx pgx.Tx, user docsbot.UserID, id docsbot.DocumentID) (skipped bool, err error) {
	res, err := q.client.InsertTx(ctx, tx, UploadDocumentArgs{UserID: string(user), DocumentID: string(id)}, uploadInsertOpts())
	if err != nil {
		return false, fmt.Errorf("insert upload job: %w", err)
	}
	return res.UniqueSkippedAsDuplicate, nil
}

// InsertSync enqueues a cloud sync of the document in tx. Duplicates are fine:
// see syncInsertOpts.
func (q *Queue) InsertSync(ctx context.Context, tx pgx.Tx, user docsbot.UserID, id docsbot.DocumentID) error {
	if _, err := q.client.InsertTx(ctx, tx, StorageSyncArgs{UserID: string(user), DocumentID: string(id)}, syncInsertOpts()); err != nil {
		return fmt.Errorf("insert sync job: %w", err)
	}
	return nil
}

// InsertTrash enqueues moving the cloud file remoteID to the provider's trash in tx.
func (q *Queue) InsertTrash(ctx context.Context, tx pgx.Tx, user docsbot.UserID, remoteID string) error {
	args := StorageTrashArgs{UserID: string(user), RemoteID: remoteID, Provider: providerGoogle}
	if _, err := q.client.InsertTx(ctx, tx, args, &river.InsertOpts{Queue: UploadQueue}); err != nil {
		return fmt.Errorf("insert trash job: %w", err)
	}
	return nil
}

// CancelUploads removes the user's pending (not yet running) upload_document
// jobs. Sync and trash jobs stay: their workers handle a disconnected Drive.
func (q *Queue) CancelUploads(ctx context.Context, user docsbot.UserID) error {
	return q.cancel(ctx, user, UploadDocumentArgs{}.Kind())
}

// CancelUserJobs removes the user's pending upload_document, storage_sync and
// storage_trash jobs.
func (q *Queue) CancelUserJobs(ctx context.Context, user docsbot.UserID) error {
	return q.cancel(ctx, user, UploadDocumentArgs{}.Kind(), StorageSyncArgs{}.Kind(), StorageTrashArgs{}.Kind())
}

func (q *Queue) cancel(ctx context.Context, user docsbot.UserID, kinds ...string) error {
	for _, kind := range kinds {
		if _, err := q.client.DeleteJobsByArg(ctx, kind, "UserID", string(user)); err != nil {
			return fmt.Errorf("cancel jobs: %w", err)
		}
	}
	return nil
}
