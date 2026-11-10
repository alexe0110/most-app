//go:build integration

package jobs_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

func TestPurgePendingWorker_deletesStalePendingOnly(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	repo := postgres.NewRepo(pool)
	var uid string
	require.NoError(t, pool.QueryRow(ctx, `insert into users (tg_user_id) values (2) returning id::text`).Scan(&uid))
	insert := func(status, unique, age string) string {
		var id string
		require.NoError(t, pool.QueryRow(ctx,
			`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, created_at)
			 values ($1::uuid, $2, 'photo', '', '', 'f', $3, now() - $4::interval) returning id::text`,
			uid, status, unique, age).Scan(&id))
		return id
	}
	stale := insert("pending", "stale", "25 hours")
	fresh := insert("pending", "fresh", "23 hours")
	oldSaved := insert("saved", "saved", "48 hours")

	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, nil, repo, repo, nil)
	workers := river.NewWorkers()
	bind, periodic := jobs.RegisterPurgePending(workers, slog.New(slog.DiscardHandler))
	bind(svc)
	queue, err := pjobs.New(pool, slog.New(slog.DiscardHandler), workers, nil, periodic)
	require.NoError(t, err)
	startQueue(t, queue)

	exists := func(id string) bool {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `select count(*) from documents where id = $1::uuid`, id).Scan(&n))
		return n == 1
	}
	require.Eventually(t, func() bool { return !exists(stale) }, 30*time.Second, 100*time.Millisecond)
	require.True(t, exists(fresh))
	require.True(t, exists(oldSaved))
}
