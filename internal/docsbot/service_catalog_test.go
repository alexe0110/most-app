package docsbot_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_Search_hugeAndOddQueries(t *testing.T) {
	repo := newFakeRepo()
	svc, _ := edgeService(repo, fakeDrive{})
	for _, q := range []string{"%", "_", `\`, "😀😀", "ДОГОВОР"} {
		found, err := svc.Search(t.Context(), user, q)
		require.NoError(t, err, q)
		require.False(t, found.BlankQuery, q)
	}
}

// searchSpy records the query that reaches the repository.
type searchSpy struct {
	*fakeRepo
	calls int
	query string
}

func (s *searchSpy) Search(ctx context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	s.calls++
	s.query = q
	return s.fakeRepo.Search(ctx, user, q, limit)
}

func TestService_Search_normalizesQuery(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantBlank bool
		wantQuery string
	}{
		{"trimmed", "  паспорт \n", false, "паспорт"},
		{"empty", "", true, ""},
		{"only spaces", "  \t\n ", true, ""},
		{"cut to 100 runes", strings.Repeat("я", 10_000), false, strings.Repeat("я", 100)},
		{"emoji counted as runes", strings.Repeat("😀", 150), false, strings.Repeat("😀", 100)},
		{"exactly 100 is kept", strings.Repeat("a", 100), false, strings.Repeat("a", 100)},
		{"metacharacters pass through unchanged", `100%_\`, false, `100%_\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &searchSpy{fakeRepo: newFakeRepo()}
			svc := docsbot.NewService(spy, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: spy.fakeRepo}, spy, newFakeStatus(spy.fakeRepo), &fakeSyncs{repo: spy.fakeRepo})

			found, err := svc.Search(t.Context(), user, tt.query)

			require.NoError(t, err)
			require.Equal(t, tt.wantBlank, found.BlankQuery)
			if tt.wantBlank {
				require.Zero(t, spy.calls, "a blank query must not hit the repository")
				return
			}
			require.Equal(t, tt.wantQuery, spy.query)
			require.Equal(t, tt.wantQuery, found.Query)
		})
	}
}

func TestService_Card_backedUpHasNoFolderLink(t *testing.T) {
	repo := newFakeRepo()
	repo.folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-1"}
	backed := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatAuto,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1"})
	tgOnly := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatAuto})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	card, err := svc.Card(t.Context(), user, backed.ID)
	require.NoError(t, err)
	require.Empty(t, card.CategoryFolderID)

	card, err = svc.Card(t.Context(), user, tgOnly.ID)
	require.NoError(t, err)
	require.Equal(t, "folder-1", card.CategoryFolderID)
}

func TestService_CategoryPage_paginates(t *testing.T) {
	repo := newFakeRepo()
	for i := 0; i < 25; i++ {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 2)
	require.NoError(t, err)
	require.Equal(t, 25, page.Total)
	require.Equal(t, 3, page.Pages)
	require.Equal(t, 2, page.Page)
	require.Len(t, page.Docs, 10)

	// An out-of-range page is clamped to the last one.
	last, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 99)
	require.NoError(t, err)
	require.Equal(t, 3, last.Page)
	require.Len(t, last.Docs, 5)
}

func TestService_Search(t *testing.T) {
	repo := newFakeRepo()
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Паспорт РК"})
	repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Договор аренды"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	found, err := svc.Search(t.Context(), user, "паспорт")
	require.NoError(t, err)
	require.Len(t, found.Docs, 1)
	require.Equal(t, "Паспорт РК", found.Docs[0].Title)
}

func TestService_folderLinks(t *testing.T) {
	tests := []struct {
		name    string
		folders map[docsbot.Category]string
		want    string
	}{
		{"known folder", map[docsbot.Category]string{docsbot.CatReceipts: "folder-1"}, "folder-1"},
		{"no folder yet", nil, ""},
		{"other category only", map[docsbot.Category]string{docsbot.CatAuto: "folder-2"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.folders = tt.folders
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			card, err := svc.Card(t.Context(), user, doc.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, card.CategoryFolderID)

			page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
			require.NoError(t, err)
			require.Equal(t, tt.want, page.FolderID)
		})
	}
}

type failingFolders struct{ err error }

func (f failingFolders) CategoryFolderID(context.Context, docsbot.UserID, docsbot.Category) (string, bool, error) {
	return "", false, f.err
}

func TestService_folderLinks_error(t *testing.T) {
	boom := errors.New("boom")
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, failingFolders{err: boom}, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.Card(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, boom)

	_, err = svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
	require.ErrorIs(t, err, boom)
}

func TestService_Card_pendingNoFolder(t *testing.T) {
	repo := newFakeRepo()
	repo.folders = map[docsbot.Category]string{docsbot.CatReceipts: "folder-1"}
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "x", TgFileUniqueID: "u"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	card, err := svc.Card(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.Empty(t, card.CategoryFolderID)
}
