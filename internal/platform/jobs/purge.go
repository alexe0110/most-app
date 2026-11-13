package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// purgeInterval is how often soft-deleted users are hard-deleted.
const purgeInterval = 24 * time.Hour

// PurgeUsersArgs is the periodic job that hard-deletes soft-deleted users. It
// carries no payload.
type PurgeUsersArgs struct{}

// Kind implements river.JobArgs.
func (PurgeUsersArgs) Kind() string { return "purge_users" }

// purgeUsersWorker removes soft-deleted users. Their bot data goes with them
// via the ON DELETE CASCADE foreign keys.
type purgeUsersWorker struct {
	river.WorkerDefaults[PurgeUsersArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Work runs the purge. Plain SQL, not a sqlc query: users is a platform table
// and platform must not import a bot's adapter (depguard).
func (w *purgeUsersWorker) Work(ctx context.Context, _ *river.Job[PurgeUsersArgs]) error {
	tag, err := w.pool.Exec(ctx, `delete from users where deleted_at is not null`)
	if err != nil {
		return fmt.Errorf("purge soft-deleted users: %w", err)
	}
	w.log.InfoContext(ctx, "purged soft-deleted users", "count", tag.RowsAffected())
	return nil
}

// RegisterPurgeUsers adds the purge_users worker and returns a daily periodic
// job. Call it before passing workers to New.
func RegisterPurgeUsers(workers *river.Workers, pool *pgxpool.Pool, log *slog.Logger) *river.PeriodicJob {
	river.AddWorker(workers, &purgeUsersWorker{pool: pool, log: log})
	return river.NewPeriodicJob(
		river.PeriodicInterval(purgeInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PurgeUsersArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: false},
	)
}
