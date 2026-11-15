//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// userSource reads users.source ("" for null).
func userSource(t *testing.T, pool *pgxpool.Pool, user docsbot.UserID) string {
	t.Helper()
	var source *string
	require.NoError(t, pool.QueryRow(t.Context(), "select source from users where id = $1", string(user)).Scan(&source))
	if source == nil {
		return ""
	}
	return *source
}

func TestEvents_Record(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	u := makeUser(t, repo, 700)
	user := docsbot.UserID(u.ID.String())
	ctx := t.Context()

	require.NoError(t, events.Record(ctx, docsbot.Event{
		User: user, Name: docsbot.EventDocSaved,
		Props: docsbot.EventProps{Category: docsbot.CatPassport, Kind: docsbot.KindPhoto},
	}))
	require.NoError(t, events.Record(ctx, docsbot.Event{User: user, Name: docsbot.EventFileInfo, Props: docsbot.EventProps{Format: docsbot.FormatHEIC}}))
	require.NoError(t, events.Record(ctx, docsbot.Event{Name: docsbot.EventDataDeleted}))

	var bot, name, props string
	require.NoError(t, pool.QueryRow(ctx,
		"select bot, name, props::text from events where user_id = $1 and name = 'doc_saved'", string(user)).Scan(&bot, &name, &props))
	require.Equal(t, docsbot.BotName, bot)
	require.Equal(t, "doc_saved", name)
	require.JSONEq(t, `{"category":"passport","kind":"photo"}`, props, "empty props are omitted")

	require.NoError(t, pool.QueryRow(ctx,
		"select props::text from events where user_id = $1 and name = 'file_info'", string(user)).Scan(&props))
	require.JSONEq(t, `{"format":"heic"}`, props)

	var anonymous int
	require.NoError(t, pool.QueryRow(ctx,
		"select count(*) from events where user_id is null and name = 'data_deleted' and props = '{}'").Scan(&anonymous))
	require.Equal(t, 1, anonymous)
}

func TestEvents_RecordStart(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 701).ID.String())

	first, err := events.RecordStart(ctx, user, "ad_1")
	require.NoError(t, err)
	require.True(t, first)
	require.Equal(t, "ad_1", userSource(t, pool, user))

	again, err := events.RecordStart(ctx, user, "ad_2")
	require.NoError(t, err)
	require.False(t, again)
	require.Equal(t, "ad_1", userSource(t, pool, user), "the first source is never overwritten")

	rows, err := pool.Query(ctx, "select props::text from events where user_id = $1 and name = 'start' order by id", string(user))
	require.NoError(t, err)
	defer rows.Close()
	var got []map[string]any
	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))
		var props map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &props))
		got = append(got, props)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []map[string]any{
		{"source": "ad_1", "new": true},
		{"source": "ad_2", "new": false},
	}, got)
}

func TestEvents_RecordStart_withoutSourceKeepsItOpen(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 702).ID.String())

	first, err := events.RecordStart(ctx, user, "")
	require.NoError(t, err)
	require.True(t, first)
	require.Empty(t, userSource(t, pool, user))

	// Attribution belongs to the first start only: a later source does not claim the user.
	_, err = events.RecordStart(ctx, user, "ad_late")
	require.NoError(t, err)
	require.Empty(t, userSource(t, pool, user))
}

func TestEvents_RecordStart_parallelStartsHaveOneFirst(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	user := docsbot.UserID(makeUser(t, repo, 703).ID.String())

	const n = 8
	firsts := make(chan bool, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			first, err := events.RecordStart(t.Context(), user, "")
			firsts <- first
			errs <- err
		}()
	}
	var count int
	for range n {
		require.NoError(t, <-errs)
		if <-firsts {
			count++
		}
	}
	require.Equal(t, 1, count)
}

// Deleting the data resets the user to a stranger: their next /start is new again
// and may claim a source, because neither the events nor the source survive.
func TestEvents_RecordStart_afterDataDeletionIsNewAgain(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 704).ID.String())

	_, err := events.RecordStart(ctx, user, "ad_1")
	require.NoError(t, err)
	require.NoError(t, repo.DeleteUserData(ctx, user))
	require.Empty(t, userSource(t, pool, user))

	// Soft-deleted and not yet revived: the lock and the insert still work.
	first, err := events.RecordStart(ctx, user, "ad_2")
	require.NoError(t, err)
	require.True(t, first, "the earlier start went with the data")
	require.Equal(t, "ad_2", userSource(t, pool, user))

	_, err = repo.Ensure(ctx, 704, "ru") // the returning user is revived
	require.NoError(t, err)
	again, err := events.RecordStart(ctx, user, "ad_3")
	require.NoError(t, err)
	require.False(t, again)
	require.Equal(t, "ad_2", userSource(t, pool, user))
}

// A user who existed before events were tracked has first_start_at backfilled by
// the migration: their /start is not new and must not give them a source.
func TestEvents_RecordStart_existingUserIsNotNew(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 705).ID.String())
	_, err := pool.Exec(ctx, `update users set first_start_at = created_at where id = $1`, string(user))
	require.NoError(t, err)

	first, err := events.RecordStart(ctx, user, "ad_1")
	require.NoError(t, err)
	require.False(t, first)
	require.Empty(t, userSource(t, pool, user))
}

// Events expire after 180 days; the user must not become new again with them.
func TestEvents_RecordStart_expiredStartsDoNotMakeTheUserNew(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	events := postgres.NewEvents(pool)
	ctx := t.Context()
	user := docsbot.UserID(makeUser(t, repo, 706).ID.String())

	first, err := events.RecordStart(ctx, user, "ad_1")
	require.NoError(t, err)
	require.True(t, first)
	_, err = pool.Exec(ctx, `delete from events`) // what the purge does to old events
	require.NoError(t, err)

	again, err := events.RecordStart(ctx, user, "ad_2")
	require.NoError(t, err)
	require.False(t, again)
	require.Equal(t, "ad_1", userSource(t, pool, user))
}

func TestEvents_failuresAreReturned(t *testing.T) {
	pool := dbtest.New(t)
	events := postgres.NewEvents(pool)
	missing := docsbot.UserID(uuid.NewString())

	tests := []struct {
		name string
		run  func() error
	}{
		{"start of a user that does not exist", func() error {
			_, err := events.RecordStart(t.Context(), missing, "ad_1")
			return err
		}},
		{"start with a malformed user id", func() error {
			_, err := events.RecordStart(t.Context(), "not-a-uuid", "")
			return err
		}},
		{"event of a user that does not exist", func() error {
			return events.Record(t.Context(), docsbot.Event{User: missing, Name: docsbot.EventSearch})
		}},
		{"event with a malformed user id", func() error {
			return events.Record(t.Context(), docsbot.Event{User: "not-a-uuid", Name: docsbot.EventSearch})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, tt.run())

			var n int
			require.NoError(t, pool.QueryRow(t.Context(), "select count(*) from events").Scan(&n))
			require.Zero(t, n, "a failed start leaves nothing behind")
		})
	}
}

func TestEvents_Record_cancelledContext(t *testing.T) {
	pool := dbtest.New(t)
	events := postgres.NewEvents(pool)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.Error(t, events.Record(ctx, docsbot.Event{Name: docsbot.EventDataDeleted}))
}
