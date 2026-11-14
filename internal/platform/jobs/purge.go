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

// eventRetention is how long product events are kept. Retention and activation
// metrics look at most a month back, so half a year is generous; anything older
// is not needed and is not kept.
const eventRetention = 180 * 24 * time.Hour

// PurgeUsersArgs is the periodic job that hard-deletes soft-deleted users. It
// carries no payload.
type PurgeUsersArgs struct{}

// Kind implements river.JobArgs.
func (PurgeUsersArgs) Kind() string { return "purge_users" }

// purgeUsersWorker removes soft-deleted users and product events past their
// retention. A user's bot data goes with them via the ON DELETE CASCADE foreign
// keys.
type purgeUsersWorker struct {
	river.WorkerDefaults[PurgeUsersArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Work runs the purge. Plain SQL, not a sqlc query: users and events are platform
// tables and platform must not import a bot's adapter (depguard). The two deletes
// are independent, so they need no shared transaction: a failure of the second
// fails the job and the first is simply a no-op on the retry.
func (w *purgeUsersWorker) Work(ctx context.Context, _ *river.Job[PurgeUsersArgs]) error {
	tag, err := w.pool.Exec(ctx, `delete from users where deleted_at is not null`)
	if err != nil {
		return fmt.Errorf("purge soft-deleted users: %w", err)
	}
	w.log.InfoContext(ctx, "purged soft-deleted users", "count", tag.RowsAffected())

	tag, err = w.pool.Exec(ctx, `delete from events where created_at < $1`, time.Now().Add(-eventRetention))
	if err != nil {
		return fmt.Errorf("purge old events: %w", err)
	}
	w.log.InfoContext(ctx, "purged old events", "count", tag.RowsAffected())
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
