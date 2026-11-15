// Package dbtest provides real Postgres for integration tests. A package's
// TestMain calls Run, which starts one container for the whole test process and
// migrates a template database once; each test then calls New to get its own
// database cloned from that template. Unit tests never use it.
package dbtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alexe0110/most-app/internal/platform/db"
	"github.com/alexe0110/most-app/internal/platform/jobs"
)

const (
	image        = "postgres:17"
	templateName = "tmpl"

	// Docker can be slow to pull and boot the image on a cold machine.
	startTimeout   = 3 * time.Minute
	migrateTimeout = time.Minute
	opTimeout      = 30 * time.Second
	stopTimeout    = time.Minute

	// Postgres allows 100 connections by default; a few per test database keeps
	// parallel tests well under that limit.
	testPoolMaxConns = 4
)

// shared holds the per-process container. It is a package-level variable on
// purpose (an exception to the no-global-state rule): go test gives TestMain
// and the tests no other way to share one container, and starting a container
// per test costs minutes. It is written once by Run before any test starts and
// is read-only afterwards, except for the atomic counter.
var shared struct {
	baseDSN string
	admin   *pgxpool.Pool
	seq     atomic.Int64
}

// Run starts one Postgres container for the package, builds the template
// database (goose and River migrations applied once), runs the tests and tears
// the container down. Call it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }
//
// It returns the exit code for os.Exit. Failing to start Docker or to migrate
// is reported on stderr and yields a non-zero code.
func Run(m *testing.M) int {
	ctr, err := start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: %v\n", err)
		return 1
	}

	code := m.Run()

	if shared.admin != nil {
		shared.admin.Close()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	if err := testcontainers.TerminateContainer(ctr, testcontainers.StopContext(stopCtx)); err != nil {
		fmt.Fprintf(os.Stderr, "dbtest: terminate container: %v\n", err)
	}
	return code
}

// New returns a pool on a fresh database cloned from the migrated template.
// The database is dropped, after the pool is closed, when the test ends. It
// fails the test if the package has no TestMain calling Run.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if shared.admin == nil {
		t.Fatal("dbtest: no container; add `func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }` to the package")
	}

	// The name comes from a counter, never from input, so it is safe to splice
	// into SQL (identifiers cannot be bound as parameters).
	name := fmt.Sprintf("t_%d", shared.seq.Add(1))

	ctx, cancel := context.WithTimeout(t.Context(), opTimeout)
	defer cancel()
	_, err := shared.admin.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName))
	if err != nil {
		t.Fatalf("dbtest: create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// t.Context is already cancelled during cleanup.
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		if _, err := shared.admin.Exec(ctx, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name)); err != nil {
			t.Errorf("dbtest: drop database %s: %v", name, err)
		}
	})

	dsn, err := dsnFor(shared.baseDSN, name)
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("dbtest: parse dsn: %v", err)
	}
	cfg.MaxConns = testPoolMaxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("dbtest: open pool: %v", err)
	}
	// Registered after the drop, so it runs before it: connections must be gone
	// before the database is dropped.
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("dbtest: ping: %v", err)
	}
	return pool
}

// start boots the container and prepares the template and admin pool. On error
// the container is already terminated.
func start() (ctr *postgres.PostgresContainer, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	ctr, err = postgres.Run(ctx, image,
		postgres.WithDatabase("most"),
		postgres.WithUsername("most"),
		postgres.WithPassword("most"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}
	defer func() {
		if err != nil {
			_ = testcontainers.TerminateContainer(ctr) // best effort; err is the real failure
			ctr = nil
		}
	}()

	baseDSN, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return ctr, fmt.Errorf("connection string: %w", err)
	}

	admin, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		return ctr, fmt.Errorf("open admin pool: %w", err)
	}
	if err := buildTemplate(ctx, admin, baseDSN); err != nil {
		admin.Close()
		return ctr, err
	}

	shared.baseDSN = baseDSN
	shared.admin = admin
	return ctr, nil
}

// buildTemplate creates the template database, migrates it and waits until
// nothing is connected to it: Postgres refuses to clone a database that has
// sessions.
func buildTemplate(ctx context.Context, admin *pgxpool.Pool, baseDSN string) error {
	ctx, cancel := context.WithTimeout(ctx, migrateTimeout)
	defer cancel()

	if _, err := admin.Exec(ctx, "CREATE DATABASE "+templateName); err != nil {
		return fmt.Errorf("create template database: %w", err)
	}
	dsn, err := dsnFor(baseDSN, templateName)
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open template pool: %w", err)
	}
	err = jobs.Migrate(ctx, pool)
	pool.Close()
	if err != nil {
		return fmt.Errorf("migrate template queue: %w", err)
	}

	return waitNoSessions(ctx, admin, templateName)
}

// waitNoSessions polls until the database has no open sessions. The client side
// closes connections before the server notices, so a short wait is expected.
func waitNoSessions(ctx context.Context, admin *pgxpool.Pool, name string) error {
	for {
		var n int
		err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname = $1", name).Scan(&n)
		if err != nil {
			return fmt.Errorf("count template sessions: %w", err)
		}
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("template database still has %d sessions: %w", n, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// dsnFor returns baseDSN pointed at another database of the same server.
func dsnFor(baseDSN, database string) (string, error) {
	u, err := url.Parse(baseDSN)
	if err != nil {
		return "", fmt.Errorf("parse dsn: %w", err)
	}
	u.Path = "/" + database
	return u.String(), nil
}
