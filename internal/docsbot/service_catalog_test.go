package docsbot_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_Search_hugeAndOddQueries(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})
	for _, q := range []string{"%", "_", `\`, "😀😀", "ДОГОВОР"} {
		found, err := svc.Search(t.Context(), user, q)
		require.NoError(t, err, q)
		require.False(t, found.BlankQuery, q)
	}
}

// searchSpy records the query that reaches the repository.
type searchSpy struct {
	*docsbottest.FakeRepo
	calls int
	query string
}

func (s *searchSpy) Search(ctx context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	s.calls++
	s.query = q
	return s.FakeRepo.Search(ctx, user, q, limit)
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
			spy := &searchSpy{FakeRepo: docsbottest.NewFakeRepo()}
			svc := docsbottest.NewService(spy, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: spy.FakeRepo}, spy, docsbottest.NewFakeStatus(spy.FakeRepo), &docsbottest.FakeSyncs{Repo: spy.FakeRepo})

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
	repo := docsbottest.NewFakeRepo()
	repo.Folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-1"}
	backed := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatAuto,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1"})
	tgOnly := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatAuto})
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	card, err := svc.Card(t.Context(), user, backed.ID)
	require.NoError(t, err)
	require.Empty(t, card.CategoryFolderID)

	card, err = svc.Card(t.Context(), user, tgOnly.ID)
	require.NoError(t, err)
	require.Equal(t, "folder-1", card.CategoryFolderID)
}

func TestService_CategoryPage_paginates(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	for i := 0; i < 25; i++ {
		repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	}
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

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
	repo := docsbottest.NewFakeRepo()
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Паспорт РК"})
	repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "Договор аренды"})
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	found, err := svc.Search(t.Context(), user, "паспорт")
	require.NoError(t, err)
	require.Len(t, found.Docs, 1)
	require.Equal(t, "Паспорт РК", found.Docs[0].Title)
}

func TestService_Search_driveStatusFailure(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	boom := errors.New("drive status down")
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{Err: boom}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	_, err := svc.Search(t.Context(), user, "паспорт")

	require.ErrorIs(t, err, boom)
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
			repo := docsbottest.NewFakeRepo()
			repo.Folders = tt.folders
			doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

			card, err := svc.Card(t.Context(), user, doc.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, card.CategoryFolderID)

			page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
			require.NoError(t, err)
			require.Equal(t, tt.want, page.FolderID)
		})
	}
}

type failingFolders struct {
	docsbot.Folders
	err error
}

func (f failingFolders) CategoryFolderID(context.Context, docsbot.UserID, docsbot.Category) (string, bool, error) {
	return "", false, f.err
}

func TestService_folderLinks_error(t *testing.T) {
	boom := errors.New("boom")
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, failingFolders{Folders: repo, err: boom}, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	_, err := svc.Card(t.Context(), user, doc.ID)
	require.ErrorIs(t, err, boom)

	_, err = svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
	require.ErrorIs(t, err, boom)
}

func TestService_Card_pendingNoFolder(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.Folders = map[docsbot.Category]string{docsbot.CatReceipts: "folder-1"}
	doc := repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "x", TgFileUniqueID: "u"})
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	card, err := svc.Card(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.Empty(t, card.CategoryFolderID)
}

func TestService_listsReportDriveConnection(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(map[bool]string{false: "drive off", true: "drive on"}[connected], func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			repo.Seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts, Title: "Чек"})
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{IsConnected: connected}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

			page, err := svc.CategoryPage(t.Context(), user, docsbot.CatReceipts, 1)
			require.NoError(t, err)
			require.Equal(t, connected, page.DriveConnected)

			found, err := svc.Search(t.Context(), user, "чек")
			require.NoError(t, err)
			require.Len(t, found.Docs, 1)
			require.Equal(t, connected, found.DriveConnected)

			cat, err := svc.Catalog(t.Context(), user)
			require.NoError(t, err)
			require.Equal(t, connected, cat.DriveConnected)
		})
	}
}

func TestService_Search_keepsQueryForTheNothingFoundText(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	found, err := svc.Search(t.Context(), user, "  Страховка  ")

	require.NoError(t, err)
	require.Empty(t, found.Docs)
	require.NotEmpty(t, found.Query)
	require.NotContains(t, found.Query, "  ", "the query is shown back to the user, trimmed")

	huge, err := svc.Search(t.Context(), user, strings.Repeat("я", 5000))
	require.NoError(t, err)
	require.LessOrEqual(t, utf8.RuneCountInString(huge.Query), 100, "an echoed query must fit a Telegram message")
}
