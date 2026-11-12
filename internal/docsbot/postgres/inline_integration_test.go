//go:build integration

package postgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// inlineEnv is a repo over a fresh database with a pool for direct fixtures.
func inlineEnv(t *testing.T) (*postgres.Repo, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.New(t)
	return postgres.NewRepo(pool), pool
}

// savedAt stores a saved document and backdates it by age, so ordering is deterministic.
func savedAt(t *testing.T, repo *postgres.Repo, pool *pgxpool.Pool, user docsbot.UserID, title string, cat docsbot.Category, age time.Duration) docsbot.Document {
	t.Helper()
	ctx := t.Context()
	d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: title, FileID: "f-" + title + string(user), FileUniqueID: "u-" + title + string(user),
	})
	require.NoError(t, err)
	mustSave(t, ctx, repo, user, d.ID, cat)
	_, err = pool.Exec(ctx, "update documents set created_at = now() - $2::interval where id = $1", string(d.ID), fmt.Sprintf("%d seconds", int(age.Seconds())))
	require.NoError(t, err)
	return d
}

func titles(docs []docsbot.Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Title
	}
	return out
}

func TestInline_enabledDefaultAndToggle(t *testing.T) {
	repo, _ := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 900).ID.String())
	other := docsbot.UserID(makeUser(t, repo, 901).ID.String())

	on, err := repo.InlineEnabled(ctx, u)
	require.NoError(t, err)
	require.True(t, on, "no row means enabled")

	require.NoError(t, repo.SetInlineEnabled(ctx, u, false))
	on, err = repo.InlineEnabled(ctx, u)
	require.NoError(t, err)
	require.False(t, on)

	require.NoError(t, repo.SetInlineEnabled(ctx, u, true))
	on, err = repo.InlineEnabled(ctx, u)
	require.NoError(t, err)
	require.True(t, on)

	require.NoError(t, repo.SetInlineEnabled(ctx, u, false))
	on, err = repo.InlineEnabled(ctx, other)
	require.NoError(t, err)
	require.True(t, on, "another user is unaffected")
}

func TestInline_searchByTitleCategoryAndIsolation(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 910).ID.String())
	other := docsbot.UserID(makeUser(t, repo, 911).ID.String())
	savedAt(t, repo, pool, u, "ОСАГО полис", docsbot.CatAuto, 3*time.Minute)
	savedAt(t, repo, pool, u, "scan", docsbot.CatPassport, 2*time.Minute)
	savedAt(t, repo, pool, u, "100%_done", docsbot.CatWork, time.Minute)
	savedAt(t, repo, pool, other, "ОСАГО чужой", docsbot.CatAuto, time.Minute)
	pending, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: u, Kind: docsbot.KindDocument, Title: "ОСАГО pending", FileID: "p", FileUniqueID: "p"})
	require.NoError(t, err)
	require.NotEmpty(t, pending.ID)

	got, err := repo.InlineSearch(ctx, u, "осаго", nil, 20, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"ОСАГО полис"}, titles(got), "title match, own and saved only")

	got, err = repo.InlineSearch(ctx, u, "паспорт", []docsbot.Category{docsbot.CatPassport}, 20, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"scan"}, titles(got), "category match")

	got, err = repo.InlineSearch(ctx, u, "осаго", []docsbot.Category{docsbot.CatPassport}, 20, 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"ОСАГО полис", "scan"}, titles(got), "title OR category")

	got, err = repo.InlineSearch(ctx, u, "%", nil, 20, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"100%_done"}, titles(got), "LIKE metacharacters match literally")

	got, err = repo.InlineSearch(ctx, other, "scan", []docsbot.Category{docsbot.CatPassport}, 20, 0)
	require.NoError(t, err)
	require.Empty(t, got, "never another user's documents")
}

func TestInline_recentSavedOrderAndOffset(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 920).ID.String())
	other := docsbot.UserID(makeUser(t, repo, 921).ID.String())
	savedAt(t, repo, pool, u, "oldest", docsbot.CatOther, 3*time.Hour)
	savedAt(t, repo, pool, u, "middle", docsbot.CatOther, 2*time.Hour)
	savedAt(t, repo, pool, u, "newest", docsbot.CatOther, time.Hour)
	savedAt(t, repo, pool, other, "foreign", docsbot.CatOther, time.Minute)

	got, err := repo.RecentSaved(ctx, u, 20, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"newest", "middle", "oldest"}, titles(got))

	got, err = repo.RecentSaved(ctx, u, 2, 1)
	require.NoError(t, err)
	require.Equal(t, []string{"middle", "oldest"}, titles(got))

	got, err = repo.RecentSaved(ctx, u, 20, 3)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestInline_lookup(t *testing.T) {
	repo, _ := inlineEnv(t)
	ctx := t.Context()
	u := makeUser(t, repo, 930)

	id, found, err := repo.Lookup(ctx, 930)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, u.ID.String(), id)

	_, found, err = repo.Lookup(ctx, 999)
	require.NoError(t, err)
	require.False(t, found, "unknown user is not created")
	_, err = repo.GetUserByTgID(ctx, 999)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	require.NoError(t, repo.DeleteUserData(ctx, docsbot.UserID(u.ID.String())))
	_, found, err = repo.Lookup(ctx, 930)
	require.NoError(t, err)
	require.False(t, found, "soft-deleted user is not found")
}

func TestInline_deleteUserDataRemovesSettings(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := makeUser(t, repo, 940)
	uid := docsbot.UserID(u.ID.String())
	require.NoError(t, repo.SetInlineEnabled(ctx, uid, false))

	require.NoError(t, repo.DeleteUserData(ctx, uid))

	var n int
	require.NoError(t, pool.QueryRow(ctx, "select count(*) from docs_settings where user_id = $1", u.ID).Scan(&n))
	require.Zero(t, n)
}

func TestInline_searchLikeMetacharacters(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 950).ID.String())
	savedAt(t, repo, pool, u, `a_b`, docsbot.CatOther, 4*time.Minute)
	savedAt(t, repo, pool, u, `axb`, docsbot.CatOther, 3*time.Minute)
	savedAt(t, repo, pool, u, `back\slash`, docsbot.CatOther, 2*time.Minute)
	savedAt(t, repo, pool, u, `plain`, docsbot.CatOther, time.Minute)

	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"underscore is literal", "a_b", []string{"a_b"}},
		{"backslash is literal", `k\s`, []string{`back\slash`}},
		{"lone backslash", `\`, []string{`back\slash`}},
		{"percent matches nothing here", "%", nil},
		{"quote does not break SQL", `' or 1=1 --`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.InlineSearch(ctx, u, tt.query, nil, 20, 0)
			require.NoError(t, err)
			if tt.want == nil {
				require.Empty(t, got)
				return
			}
			require.Equal(t, tt.want, titles(got))
		})
	}
}

func TestInline_searchNoDuplicateWhenTitleAndCategoryMatch(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 951).ID.String())
	savedAt(t, repo, pool, u, "Паспорт папы", docsbot.CatPassport, time.Minute)

	got, err := repo.InlineSearch(ctx, u, "паспорт", []docsbot.Category{docsbot.CatPassport}, 20, 0)

	require.NoError(t, err)
	require.Equal(t, []string{"Паспорт папы"}, titles(got))
}

// Equal created_at must not make pages overlap or skip rows.
func TestInline_paginationStableWithEqualCreatedAt(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 952).ID.String())
	for i := range 7 {
		savedAt(t, repo, pool, u, fmt.Sprintf("same %d", i), docsbot.CatOther, time.Hour)
	}

	collect := func(fetch func(limit, off int) ([]docsbot.Document, error)) []string {
		var all []string
		for off := 0; off < 20; off += 3 {
			page, err := fetch(3, off)
			require.NoError(t, err)
			all = append(all, titles(page)...)
		}
		return all
	}
	recent := collect(func(l, o int) ([]docsbot.Document, error) { return repo.RecentSaved(ctx, u, l, o) })
	require.Len(t, recent, 7)
	require.Len(t, uniq(recent), 7, "no duplicates across pages: %v", recent)

	found := collect(func(l, o int) ([]docsbot.Document, error) {
		return repo.InlineSearch(ctx, u, "same", nil, l, o)
	})
	require.Len(t, found, 7)
	require.Len(t, uniq(found), 7, "no duplicates across pages: %v", found)
}

func uniq(in []string) map[string]struct{} {
	m := make(map[string]struct{}, len(in))
	for _, s := range in {
		m[s] = struct{}{}
	}
	return m
}

func TestInline_softDeletedDocumentsHidden(t *testing.T) {
	repo, pool := inlineEnv(t)
	ctx := t.Context()
	u := docsbot.UserID(makeUser(t, repo, 953).ID.String())
	savedAt(t, repo, pool, u, "keep", docsbot.CatOther, time.Minute)

	require.NoError(t, repo.DeleteUserData(ctx, u))

	got, err := repo.RecentSaved(ctx, u, 20, 0)
	require.NoError(t, err)
	require.Empty(t, got, "deleted user's documents are not offered")
}
