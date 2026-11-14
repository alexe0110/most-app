//go:build integration

package postgres_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

var metricName = regexp.MustCompile(`(?m)^-- name: (\w+)\s*$`)

// metricQueries splits docs/metrics.sql into its named queries.
func metricQueries(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/metrics.sql")
	require.NoError(t, err)
	text := string(raw)
	marks := metricName.FindAllStringSubmatchIndex(text, -1)
	require.NotEmpty(t, marks)
	out := map[string]string{}
	for i, m := range marks {
		end := len(text)
		if i+1 < len(marks) {
			end = marks[i+1][0]
		}
		out[text[m[2]:m[3]]] = strings.TrimSpace(text[m[1]:end])
	}
	return out
}

// runMetric runs a metrics query and returns its rows as JSON objects, so numeric
// columns compare as plain numbers.
func runMetric(t *testing.T, pool *pgxpool.Pool, query string) []map[string]any {
	t.Helper()
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	rows, err := pool.Query(t.Context(), "select to_jsonb(q) from ("+query+") q")
	require.NoError(t, err)
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		var row map[string]any
		require.NoError(t, json.Unmarshal(raw, &row))
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	return out
}

// sum adds up a numeric column over all rows, so a check does not depend on which
// week a seeded event happens to fall into.
func sum(rows []map[string]any, col string) float64 {
	var total float64
	for _, r := range rows {
		if v, ok := r[col].(float64); ok {
			total += v
		}
	}
	return total
}

func seedMetrics(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	var u1, u2, u3 string
	for _, u := range []struct {
		id     *string
		tgID   int64
		source any
	}{{&u1, 1, "ad_1"}, {&u2, 2, "ad_1"}, {&u3, 3, nil}} {
		require.NoError(t, pool.QueryRow(ctx,
			`insert into users (tg_user_id, source) values ($1, $2) returning id::text`, u.tgID, u.source).Scan(u.id))
	}
	// ago is how long before now the event happened.
	ev := func(user, name, props, ago string) {
		_, err := pool.Exec(ctx,
			`insert into events (user_id, bot, name, props, created_at) values ($1::uuid, 'docs', $2, $3::jsonb, now() - $4::interval)`,
			user, name, props, ago)
		require.NoError(t, err)
	}
	// u1: starts 40 days ago, saves a document the next day, connects Drive, comes
	// back on days 1, 7 and 30 after the first start.
	ev(u1, "start", `{"source":"ad_1","new":true}`, "40 days")
	ev(u1, "doc_saved", `{"category":"passport","kind":"photo"}`, "39 days")
	ev(u1, "drive_connected", `{}`, "20 days")
	ev(u1, "search", `{}`, "33 days")
	ev(u1, "search", `{}`, "10 days")
	// u2: starts the same day and never returns.
	ev(u2, "start", `{"source":"ad_1","new":true}`, "40 days")
	// u3: starts two days ago without a source, saves a document the next day.
	ev(u3, "start", `{"new":true}`, "2 days")
	ev(u3, "doc_saved", `{"category":"other","kind":"document"}`, "1 day")
	ev(u3, "inline_sent", `{}`, "1 day")
	ev(u3, "convert_done", `{"operation":"compress"}`, "1 day")
	ev(u3, "export", `{}`, "1 day")
	ev(u3, "search", `{}`, "1 day")
}

func TestMetrics(t *testing.T) {
	pool := dbtest.New(t)
	seedMetrics(t, pool)
	queries := metricQueries(t)

	require.ElementsMatch(t,
		[]string{"sources_daily", "sources_weekly", "activation", "drive_funnel", "feature_usage", "retention"},
		keys(queries))

	t.Run("sources_daily", func(t *testing.T) {
		rows := runMetric(t, pool, queries["sources_daily"])
		require.Len(t, rows, 1, "only the start two days ago is within 30 days")
		require.Equal(t, "(none)", rows[0]["source"])
		require.EqualValues(t, 1, rows[0]["new_users"])
	})

	t.Run("sources_weekly", func(t *testing.T) {
		bySource := map[string]float64{}
		for _, r := range runMetric(t, pool, queries["sources_weekly"]) {
			bySource[r["source"].(string)] += r["new_users"].(float64)
		}
		require.Equal(t, map[string]float64{"ad_1": 2, "(none)": 1}, bySource)
	})

	t.Run("activation", func(t *testing.T) {
		rows := runMetric(t, pool, queries["activation"])
		require.EqualValues(t, 3, sum(rows, "new_users"))
		require.EqualValues(t, 2, sum(rows, "activated_users"))
	})

	t.Run("drive_funnel", func(t *testing.T) {
		rows := runMetric(t, pool, queries["drive_funnel"])
		require.EqualValues(t, 2, sum(rows, "users_with_doc"))
		require.EqualValues(t, 1, sum(rows, "drive_connected_users"))
	})

	t.Run("feature_usage", func(t *testing.T) {
		rows := runMetric(t, pool, queries["feature_usage"])
		require.EqualValues(t, 1, sum(rows, "inline_sent_users"))
		require.EqualValues(t, 1, sum(rows, "convert_done_users"))
		require.EqualValues(t, 1, sum(rows, "export_users"))
		require.EqualValues(t, 3, sum(rows, "search_users"), "u1 searched in two different weeks, u3 in one")
	})

	t.Run("retention", func(t *testing.T) {
		rows := runMetric(t, pool, queries["retention"])
		require.Len(t, rows, 2)
		// Newest cohort first: u3 came back on day 1, days 7 and 30 have not happened yet.
		require.EqualValues(t, 1, rows[0]["new_users"])
		require.EqualValues(t, 100, rows[0]["d1_pct"])
		require.Nil(t, rows[0]["d7_pct"])
		require.Nil(t, rows[0]["d30_pct"])
		// u1 returned on days 1, 7 and 30; u2 never did.
		require.EqualValues(t, 2, rows[1]["new_users"])
		require.EqualValues(t, 50, rows[1]["d1_pct"])
		require.EqualValues(t, 50, rows[1]["d7_pct"])
		require.EqualValues(t, 50, rows[1]["d30_pct"])
	})
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
