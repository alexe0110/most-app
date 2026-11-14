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

func TestPurgePendingWorker_deletesStalePendingAndClosedStatusMessagesOnly(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	repo := postgres.NewDocuments(pool, nil)
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
	status := func(closed string) string {
		var id string
		require.NoError(t, pool.QueryRow(ctx,
			`insert into status_messages (user_id, chat_id, message_id, created_at, closed_at)
			 values ($1::uuid, 1, 1, now() - interval '72 hours', case when $2::text = '' then null else now() - $2::text::interval end) returning id::text`,
			uid, closed).Scan(&id))
		return id
	}
	oldClosed := status("30 hours")
	recentClosed := status("1 hour")
	stillOpen := status("")
	_, err := pool.Exec(ctx, `update documents set status_message_id = $1::uuid where id = $2::uuid`, oldClosed, oldSaved)
	require.NoError(t, err)

	svc := docsbot.NewService(docsbot.Ports{Repo: repo, Drive: fakeDrive{}, Files: fakeFiles{}, Folders: postgres.NewFolderCache(pool), Status: postgres.NewStatusMessages(pool)})
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
	statusExists := func(id string) bool {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `select count(*) from status_messages where id = $1::uuid`, id).Scan(&n))
		return n == 1
	}
	require.Eventually(t, func() bool { return !exists(stale) && !statusExists(oldClosed) }, 30*time.Second, 100*time.Millisecond)
	require.True(t, exists(fresh))
	require.True(t, exists(oldSaved), "a document outlives its purged status message")
	require.True(t, statusExists(recentClosed))
	require.True(t, statusExists(stillOpen), "an open status message is never purged")
}
