package docsbot_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func ids(docs []docsbot.Document) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Title
	}
	return out
}

func TestService_Inline(t *testing.T) {
	const stranger docsbot.UserID = "user-2"
	saved := func(u docsbot.UserID, title string, cat docsbot.Category) docsbot.Document {
		return docsbot.Document{UserID: u, Status: docsbot.StatusSaved, Title: title, Category: cat}
	}

	tests := []struct {
		name       string
		seed       []docsbot.Document
		off        bool
		user       docsbot.UserID
		query      string
		offset     string
		wantState  docsbot.InlineState
		wantTitles []string
		wantNext   string
	}{
		{
			name:       "empty query lists own recent, newest first, never foreign",
			seed:       []docsbot.Document{saved(user, "first", docsbot.CatOther), saved(stranger, "foreign", docsbot.CatOther), saved(user, "second", docsbot.CatOther)},
			user:       user,
			wantTitles: []string{"second", "first"},
		},
		{
			name:       "title match is scoped to the user",
			seed:       []docsbot.Document{saved(user, "ОСАГО полис", docsbot.CatAuto), saved(stranger, "ОСАГО чужой", docsbot.CatAuto)},
			user:       user,
			query:      "осаго",
			wantTitles: []string{"ОСАГО полис"},
		},
		{
			name:       "category label match",
			seed:       []docsbot.Document{saved(user, "scan 1", docsbot.CatPassport), saved(user, "scan 2", docsbot.CatWork), saved(stranger, "scan 3", docsbot.CatPassport)},
			user:       user,
			query:      "паспорт",
			wantTitles: []string{"scan 1"},
		},
		{
			name:      "nothing found is still OK",
			seed:      []docsbot.Document{saved(user, "scan", docsbot.CatWork)},
			user:      user,
			query:     "zzz",
			wantState: docsbot.InlineOK,
		},
		{
			name:      "no documents at all",
			seed:      []docsbot.Document{saved(stranger, "foreign", docsbot.CatOther)},
			user:      user,
			wantState: docsbot.InlineEmpty,
		},
		{
			name:      "pending documents do not count",
			seed:      []docsbot.Document{{UserID: user, Status: docsbot.StatusPending, Title: "p"}},
			user:      user,
			wantState: docsbot.InlineEmpty,
		},
		{
			name:      "disabled",
			seed:      []docsbot.Document{saved(user, "a", docsbot.CatOther)},
			off:       true,
			user:      user,
			wantState: docsbot.InlineDisabled,
		},
		{
			name:      "unknown user",
			seed:      []docsbot.Document{saved(stranger, "a", docsbot.CatOther)},
			user:      "",
			wantState: docsbot.InlineUnknown,
		},
		{
			name:       "invalid offset starts from the top",
			seed:       []docsbot.Document{saved(user, "a", docsbot.CatOther)},
			user:       user,
			offset:     "abc",
			wantTitles: []string{"a"},
		},
		{
			name:       "negative offset starts from the top",
			seed:       []docsbot.Document{saved(user, "a", docsbot.CatOther)},
			user:       user,
			offset:     "-5",
			wantTitles: []string{"a"},
		},
		{
			name:      "offset past the end is OK, not empty",
			seed:      []docsbot.Document{saved(user, "a", docsbot.CatOther)},
			user:      user,
			offset:    "20",
			wantState: docsbot.InlineOK,
		},
		{
			name:      "offset is capped",
			seed:      []docsbot.Document{saved(user, "a", docsbot.CatOther)},
			user:      user,
			offset:    "999999999999",
			wantState: docsbot.InlineOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for _, d := range tt.seed {
				repo.seed(d)
			}
			if tt.off {
				require.NoError(t, repo.SetInlineEnabled(t.Context(), user, false))
			}
			svc, _ := edgeService(repo, fakeDrive{})

			res, err := svc.Inline(t.Context(), tt.user, tt.query, tt.offset)

			require.NoError(t, err)
			require.Equal(t, tt.wantState, res.State)
			if tt.wantTitles == nil {
				require.Empty(t, res.Docs)
			} else {
				require.Equal(t, tt.wantTitles, ids(res.Docs))
			}
			require.Equal(t, tt.wantNext, res.NextOffset)
		})
	}
}

func TestService_Inline_pagination(t *testing.T) {
	repo := newFakeRepo()
	for i := range 45 {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: fmt.Sprintf("doc %02d", i), Category: docsbot.CatOther})
	}
	svc, _ := edgeService(repo, fakeDrive{})

	p1, err := svc.Inline(t.Context(), user, "", "")
	require.NoError(t, err)
	require.Len(t, p1.Docs, 20)
	require.Equal(t, "20", p1.NextOffset)
	require.Equal(t, "doc 44", p1.Docs[0].Title)

	p2, err := svc.Inline(t.Context(), user, "", p1.NextOffset)
	require.NoError(t, err)
	require.Len(t, p2.Docs, 20)
	require.Equal(t, "40", p2.NextOffset)
	require.Equal(t, "doc 24", p2.Docs[0].Title)

	p3, err := svc.Inline(t.Context(), user, "", p2.NextOffset)
	require.NoError(t, err)
	require.Len(t, p3.Docs, 5)
	require.Empty(t, p3.NextOffset, "last page has no next offset")

	// Exactly one full page: no next page.
	repo2 := newFakeRepo()
	for range 20 {
		repo2.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "x", Category: docsbot.CatOther})
	}
	svc2, _ := edgeService(repo2, fakeDrive{})
	full, err := svc2.Inline(t.Context(), user, "x", "")
	require.NoError(t, err)
	require.Len(t, full.Docs, 20)
	require.Empty(t, full.NextOffset)
}

func TestService_Inline_unknownUserReadsNothing(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})

	res, err := svc.Inline(t.Context(), "", "q", "")

	require.NoError(t, err)
	require.Equal(t, docsbot.InlineUnknown, res.State)
}

func TestService_SettingsInlineSwitch(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})

	view, err := svc.Settings(t.Context(), user)
	require.NoError(t, err)
	require.True(t, view.InlineEnabled, "on by default")

	require.NoError(t, svc.SetInlineEnabled(t.Context(), user, false))
	view, err = svc.Settings(t.Context(), user)
	require.NoError(t, err)
	require.False(t, view.InlineEnabled)
}

func TestService_Inline_queryNormalization(t *testing.T) {
	saved := func(title string) docsbot.Document {
		return docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: title, Category: docsbot.CatOther}
	}
	tests := []struct {
		name  string
		seed  []docsbot.Document
		query string
		want  []string
	}{
		{"blank query is recent", []docsbot.Document{saved("a"), saved("b")}, "   \t ", []string{"b", "a"}},
		{"query is trimmed", []docsbot.Document{saved("alpha")}, "  alpha ", []string{"alpha"}},
		{"query over 100 runes is truncated", []docsbot.Document{saved(strings.Repeat("я", 100))}, strings.Repeat("я", 500), []string{strings.Repeat("я", 100)}},
		{"category label is case-insensitive", []docsbot.Document{{UserID: user, Status: docsbot.StatusSaved, Title: "scan", Category: docsbot.CatPassport}}, "ПАСПОРТ", []string{"scan"}},
		{"title and category match returns the row once", []docsbot.Document{{UserID: user, Status: docsbot.StatusSaved, Title: "Паспорт папы", Category: docsbot.CatPassport}}, "паспорт", []string{"Паспорт папы"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for _, d := range tt.seed {
				repo.seed(d)
			}
			svc, _ := edgeService(repo, fakeDrive{})

			res, err := svc.Inline(t.Context(), user, tt.query, "")

			require.NoError(t, err)
			require.Equal(t, docsbot.InlineOK, res.State)
			require.Equal(t, tt.want, ids(res.Docs))
		})
	}
}

func TestService_Inline_boundaries(t *testing.T) {
	for _, n := range []int{19, 20, 21} {
		t.Run(fmt.Sprintf("%d docs", n), func(t *testing.T) {
			repo := newFakeRepo()
			for range n {
				repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "x", Category: docsbot.CatOther})
			}
			svc, _ := edgeService(repo, fakeDrive{})

			res, err := svc.Inline(t.Context(), user, "", "")

			require.NoError(t, err)
			require.Len(t, res.Docs, min(n, 20))
			if n > 20 {
				require.Equal(t, "20", res.NextOffset)
			} else {
				require.Empty(t, res.NextOffset)
			}
		})
	}
}

// errInlineRepo fails the chosen inline-related call.
type errInlineRepo struct {
	*fakeRepo
	failEnabled, failSearch, failRecent, failCount bool
	err                                            error
}

func (r errInlineRepo) InlineEnabled(ctx context.Context, u docsbot.UserID) (bool, error) {
	if r.failEnabled {
		return false, r.err
	}
	return r.fakeRepo.InlineEnabled(ctx, u)
}

func (r errInlineRepo) InlineSearch(ctx context.Context, u docsbot.UserID, q string, c []docsbot.Category, l, o int) ([]docsbot.Document, error) {
	if r.failSearch {
		return nil, r.err
	}
	return r.fakeRepo.InlineSearch(ctx, u, q, c, l, o)
}

func (r errInlineRepo) RecentSaved(ctx context.Context, u docsbot.UserID, l, o int) ([]docsbot.Document, error) {
	if r.failRecent {
		return nil, r.err
	}
	return r.fakeRepo.RecentSaved(ctx, u, l, o)
}

func (r errInlineRepo) CountSaved(ctx context.Context, u docsbot.UserID) (int, error) {
	if r.failCount {
		return 0, r.err
	}
	return r.fakeRepo.CountSaved(ctx, u)
}

func TestService_Inline_repoErrors(t *testing.T) {
	boom := errors.New("db down")
	tests := []struct {
		name  string
		repo  errInlineRepo
		query string
	}{
		{"inline enabled", errInlineRepo{failEnabled: true}, ""},
		{"inline search", errInlineRepo{failSearch: true}, "q"},
		{"recent saved", errInlineRepo{failRecent: true}, ""},
		{"count saved", errInlineRepo{failCount: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.repo.fakeRepo, tt.repo.err = newFakeRepo(), boom // no documents: CountSaved is reached
			svc := docsbot.NewService(tt.repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: tt.repo.fakeRepo}, tt.repo, newFakeStatus(tt.repo.fakeRepo), &fakeSyncs{repo: tt.repo.fakeRepo})

			_, err := svc.Inline(t.Context(), user, tt.query, "")

			require.ErrorIs(t, err, boom)
		})
	}
}
