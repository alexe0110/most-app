package docsbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
