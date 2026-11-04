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

// PurgeUsersArgs is the periodic job that hard-deletes users marked for
// deletion. It carries no payload — the job always purges every soft-deleted
// user.
type PurgeUsersArgs struct{}

// Kind implements river.JobArgs.
func (PurgeUsersArgs) Kind() string { return "purge_users" }

// purgeUsersWorker removes soft-deleted users. Their bot data — documents,
// integrations, drive folders — goes with them through the ON DELETE CASCADE
// foreign keys, so a single statement cleans everything up.
type purgeUsersWorker struct {
	river.WorkerDefaults[PurgeUsersArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Work runs the purge. The statement is plain SQL rather than a sqlc query:
// users is a platform-wide table and platform code must not import a bot's
// postgres adapter (enforced by depguard).
func (w *purgeUsersWorker) Work(ctx context.Context, _ *river.Job[PurgeUsersArgs]) error {
	tag, err := w.pool.Exec(ctx, `delete from users where deleted_at is not null`)
	if err != nil {
		return fmt.Errorf("purge soft-deleted users: %w", err)
	}
	w.log.Info("purged soft-deleted users", "count", tag.RowsAffected())
	return nil
}

// RegisterPurgeUsers registers the purge_users worker on workers and returns a
// periodic job that runs it once a day. Call it before passing workers to New.
func RegisterPurgeUsers(workers *river.Workers, pool *pgxpool.Pool, log *slog.Logger) *river.PeriodicJob {
	river.AddWorker(workers, &purgeUsersWorker{pool: pool, log: log})
	return river.NewPeriodicJob(
		river.PeriodicInterval(purgeInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PurgeUsersArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: false},
	)
}
