// Package jobs wraps the Postgres-backed River job queue shared by every bot.
// Jobs survive restarts and can be enqueued in the same transaction as the
// change that triggers them.
package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
)

// Queue defaults; retries use River's default backoff.
const (
	maxWorkers  = 5
	maxAttempts = 5
	jobTimeout  = 5 * time.Minute
)

// Client is a thin façade over a River client: start/stop the workers and
// enqueue jobs. River is not exposed beyond its job-args and option types.
type Client struct {
	river *river.Client[pgx.Tx]
}

// New builds a River client bound to pool. workers must have at least one
// worker registered; periodic lists scheduled jobs. Migrate must run first.
func New(pool *pgxpool.Pool, log *slog.Logger, workers *river.Workers, periodic ...*river.PeriodicJob) (*Client, error) {
	rc, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: maxWorkers}},
		Workers:      workers,
		PeriodicJobs: periodic,
		MaxAttempts:  maxAttempts,
		JobTimeout:   jobTimeout,
		Logger:       log,
	})
	if err != nil {
		return nil, fmt.Errorf("new river client: %w", err)
	}
	return &Client{river: rc}, nil
}

// Start begins working jobs. Cancel ctx later for a graceful shutdown.
func (c *Client) Start(ctx context.Context) error {
	if err := c.river.Start(ctx); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	return nil
}

// Stop drains in-flight jobs, giving up when ctx is done.
func (c *Client) Stop(ctx context.Context) error {
	if err := c.river.Stop(ctx); err != nil {
		return fmt.Errorf("stop river: %w", err)
	}
	return nil
}

// Insert enqueues a job on its own connection.
func (c *Client) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	res, err := c.river.Insert(ctx, args, opts)
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}
	return res, nil
}

// InsertTx enqueues a job inside tx, so the job and the data change commit or
// roll back together.
func (c *Client) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	res, err := c.river.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return nil, fmt.Errorf("insert job in tx: %w", err)
	}
	return res, nil
}

// Migrate brings the River queue tables up to date. Run it before New.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("new river migrator: %w", err)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("river migrate up: %w", err)
	}
	return nil
}
