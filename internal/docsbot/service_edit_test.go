package docsbot_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_SetTitleCategoryDelete_missingIsNotFound(t *testing.T) {
	repo := newFakeRepo()
	foreign := repo.seed(docsbot.Document{UserID: stranger, Status: docsbot.StatusSaved, Title: "x", Category: docsbot.CatOther})
	svc, _ := edgeService(repo, fakeDrive{})

	_, err := svc.Rename(t.Context(), user, foreign.ID, "new")
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.Recategorize(t.Context(), user, foreign.ID, docsbot.CatPassport)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.Delete(t.Context(), user, foreign.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.Delete(t.Context(), user, "nope")
	require.ErrorIs(t, err, docsbot.ErrNotFound)

	require.Equal(t, "x", repo.docs[foreign.ID].Title, "foreign document untouched")
}

func TestService_parallelDelete(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(int) error { _, err := svc.Delete(t.Context(), user, doc.ID); return err })

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
			continue
		}
		require.ErrorIs(t, err, docsbot.ErrNotFound)
	}
	require.Equal(t, 1, ok, "exactly one delete wins, the rest see the document gone")
}

func TestService_parallelDeleteAndRename(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(i int) error {
		if i == 10 {
			_, err := svc.Delete(t.Context(), user, doc.ID)
			return err
		}
		_, err := svc.Rename(t.Context(), user, doc.ID, "new")
		return err
	})

	for _, err := range errs {
		if err != nil {
			require.ErrorIs(t, err, docsbot.ErrNotFound)
		}
	}
	require.NotContains(t, repo.docs, doc.ID)
}

func TestService_parallelRecategorize(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(i int) error {
		cat := docsbot.CatPassport
		if i%2 == 0 {
			cat = docsbot.CatReceipts
		}
		_, err := svc.Recategorize(t.Context(), user, doc.ID, cat)
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Contains(t, []docsbot.Category{docsbot.CatPassport, docsbot.CatReceipts}, repo.docs[doc.ID].Category)
}

func TestService_parallelRename(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc, _ := edgeService(repo, fakeDrive{})

	errs := runParallel(20, func(int) error {
		_, err := svc.Rename(t.Context(), user, doc.ID, "new")
		return err
	})

	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, "new", repo.docs[doc.ID].Title)
}

func TestService_Rename_normalizes(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.Rename(t.Context(), user, doc.ID, "\n\u200b \t")
	require.ErrorIs(t, err, docsbot.ErrEmptyTitle)
	require.Equal(t, "old", repo.docs[doc.ID].Title, "old title stays")

	card, err := svc.Rename(t.Context(), user, doc.ID, "  a\nb  "+strings.Repeat("я", docsbot.MaxTitleRunes-4))
	require.NoError(t, err)
	require.Equal(t, "a b "+strings.Repeat("я", docsbot.MaxTitleRunes-4), card.Doc.Title)
}

func TestService_Rename_tooLongIsRefusedNotCut(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantErr error
	}{
		{"exactly the limit", strings.Repeat("я", docsbot.MaxTitleRunes), nil},
		{"one over", strings.Repeat("я", docsbot.MaxTitleRunes+1), docsbot.ErrTitleTooLong},
		{"far over", strings.Repeat("я", 500), docsbot.ErrTitleTooLong},
		{"whitespace does not count", "  " + strings.Repeat("я ", 3) + "\n" + strings.Repeat(" ", 300), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			_, err := svc.Rename(t.Context(), user, doc.ID, tt.title)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, "old", repo.docs[doc.ID].Title, "nothing is stored")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestService_Recategorize_driveFileStays(t *testing.T) {
	tests := []struct {
		name       string
		doc        docsbot.Document
		wantNote   bool
		wantFolder string
	}{
		{"backed up: file stays, no new folder link",
			docsbot.Document{BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1"}, true, ""},
		{"telegram only: plain move, folder link as usual",
			docsbot.Document{BackupStatus: docsbot.BackupNone}, false, "folder-new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-new"}
			tt.doc.UserID, tt.doc.Status, tt.doc.Category = user, docsbot.StatusSaved, docsbot.CatOther
			doc := repo.seed(tt.doc)
			svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

			card, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatAuto)
			require.NoError(t, err)
			require.Equal(t, docsbot.CatAuto, card.Doc.Category)
			require.Equal(t, tt.wantNote, card.FileInOldFolder)
			require.Equal(t, tt.wantFolder, card.CategoryFolderID)
			require.Equal(t, tt.doc.DriveFileID, card.Doc.DriveFileID, "the Drive file id is untouched")
		})
	}
}

func TestService_Recategorize_sameCategoryHasNoOldFolderNote(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1",
	})
	svc, _ := edgeService(repo, fakeDrive{})

	card, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatOther)

	require.NoError(t, err)
	require.False(t, card.FileInOldFolder)
}

func TestService_Rename(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.Rename(t.Context(), user, doc.ID, "   ")
	require.ErrorIs(t, err, docsbot.ErrEmptyTitle)

	card, err := svc.Rename(t.Context(), user, doc.ID, "  Новый паспорт  ")
	require.NoError(t, err)
	require.Equal(t, "Новый паспорт", card.Doc.Title)
}

func TestService_Recategorize_rejectsUnknown(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), &fakeSyncs{repo: repo})

	_, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.Category("nope"))
	require.ErrorIs(t, err, docsbot.ErrUnknownCategory)
}
