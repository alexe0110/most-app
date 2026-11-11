package docsbot_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestCategory_Label(t *testing.T) {
	tests := []struct {
		slug docsbot.Category
		want string
	}{
		{docsbot.CatPassport, "Паспорт и ID"},
		{docsbot.CatCertificates, "Справки"},
		{docsbot.CatWork, "Работа"},
		{docsbot.CatOther, "Другое"},
		{"contracts", "Другое"}, // retired slug
		{"", "Другое"},
	}
	for _, tt := range tests {
		t.Run(string(tt.slug), func(t *testing.T) {
			require.Equal(t, tt.want, tt.slug.Label())
			require.Equal(t, tt.want, tt.slug.FolderName())
		})
	}
}

func TestParseCategory(t *testing.T) {
	for _, c := range docsbot.AllCategories {
		got, ok := docsbot.ParseCategory(string(c))
		require.True(t, ok, string(c))
		require.Equal(t, c, got)
	}
	_, ok := docsbot.ParseCategory("edu_work")
	require.False(t, ok)
	require.Equal(t, docsbot.CatOther, docsbot.AllCategories[len(docsbot.AllCategories)-1])
}

func TestService_Catalog_foldsRetiredSlugs(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "a", Category: "contracts"})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "b", Category: docsbot.CatOther})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "c", Category: docsbot.CatWork})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "d", Category: docsbot.CatPassport})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	cat, err := svc.Catalog(t.Context(), user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.CategoryCount{
		{Category: docsbot.CatPassport, Count: 1},
		{Category: docsbot.CatWork, Count: 1},
		{Category: docsbot.CatOther, Count: 2},
	}, cat.Counts)

	page, err := svc.CategoryPage(t.Context(), user, docsbot.CatOther, 1)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Len(t, page.Docs, 2)
}
