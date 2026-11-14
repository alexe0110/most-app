package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// JobInserter enqueues jobs inside the caller's transaction, so a row change and
// its job commit or roll back together. Declared here, implemented by
// docsbot/jobs (postgres must not import River's adapter).
type JobInserter interface {
	// InsertUpload enqueues the document's Drive upload. skipped reports that a
	// live upload job already exists, so nothing was inserted.
	InsertUpload(ctx context.Context, tx pgx.Tx, user docsbot.UserID, id docsbot.DocumentID) (skipped bool, err error)
	// InsertSync enqueues a cloud sync of the document.
	InsertSync(ctx context.Context, tx pgx.Tx, user docsbot.UserID, id docsbot.DocumentID) error
	// InsertTrash enqueues moving the cloud file remoteID to the provider's trash.
	InsertTrash(ctx context.Context, tx pgx.Tx, user docsbot.UserID, remoteID string) error
}
