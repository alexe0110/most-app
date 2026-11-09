package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// PurgeOAuthStatesArgs is the periodic job that deletes expired OAuth states.
// It carries no payload.
type PurgeOAuthStatesArgs struct{}

// Kind implements river.JobArgs.
func (PurgeOAuthStatesArgs) Kind() string { return "purge_oauth_states" }

// purgeOAuthStatesWorker removes OAuth states that outlived their TTL; a state
// is otherwise deleted only when the callback consumes it.
type purgeOAuthStatesWorker struct {
	river.WorkerDefaults[PurgeOAuthStatesArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
}

// Work runs the purge. Plain SQL for the same reason as purge_users.
func (w *purgeOAuthStatesWorker) Work(ctx context.Context, _ *river.Job[PurgeOAuthStatesArgs]) error {
	tag, err := w.pool.Exec(ctx, `delete from oauth_states where expires_at < now()`)
	if err != nil {
		return fmt.Errorf("purge expired oauth states: %w", err)
	}
	w.log.Info("purged expired oauth states", "count", tag.RowsAffected())
	return nil
}

// RegisterPurgeOAuthStates adds the purge_oauth_states worker and returns a
// daily periodic job. Call it before passing workers to New.
func RegisterPurgeOAuthStates(workers *river.Workers, pool *pgxpool.Pool, log *slog.Logger) *river.PeriodicJob {
	river.AddWorker(workers, &purgeOAuthStatesWorker{pool: pool, log: log})
	return river.NewPeriodicJob(
		river.PeriodicInterval(purgeInterval),
		func() (river.JobArgs, *river.InsertOpts) { return PurgeOAuthStatesArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: false},
	)
}
