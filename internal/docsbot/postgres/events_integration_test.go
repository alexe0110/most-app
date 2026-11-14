//go:build integration

package postgres_test

import (
	"encoding/json"
	"testing"

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
	require.NoError(t, events.Record(ctx, docsbot.Event{Name: docsbot.EventDataDeleted}))

	var bot, name, props string
	require.NoError(t, pool.QueryRow(ctx,
		"select bot, name, props::text from events where user_id = $1", string(user)).Scan(&bot, &name, &props))
	require.Equal(t, docsbot.BotName, bot)
	require.Equal(t, "doc_saved", name)
	require.JSONEq(t, `{"category":"passport","kind":"photo"}`, props, "empty props are omitted")

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
