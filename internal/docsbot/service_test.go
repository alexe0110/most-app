package docsbot_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

const user docsbot.UserID = "user-1"

func incoming(unique string) docsbot.IncomingFile {
	return docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "f-" + unique,
		FileUniqueID: unique, Title: "Паспорт",
	}
}

func TestService_Receive(t *testing.T) {
	saved := docsbot.Document{
		ID: "d-saved", UserID: user, Status: docsbot.StatusSaved,
		Category: docsbot.CatPassport, Title: "Паспорт", TgFileUniqueID: "dup-saved",
	}
	pending := docsbot.Document{
		ID: "d-pending", UserID: user, Status: docsbot.StatusPending,
		Title: "Паспорт", TgFileUniqueID: "dup-pending",
	}

	tests := []struct {
		name   string
		seed   *docsbot.Document
		unique string
		assert func(t *testing.T, rep docsbot.Reply, repo *fakeRepo)
	}{
		{
			name:   "new file is created and needs a category",
			unique: "fresh",
			assert: func(t *testing.T, rep docsbot.Reply, repo *fakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.StatusPending, nc.Doc.Status)
				require.Len(t, repo.docs, 1)
			},
		},
		{
			name:   "pending duplicate re-asks the category",
			seed:   &pending,
			unique: "dup-pending",
			assert: func(t *testing.T, rep docsbot.Reply, repo *fakeRepo) {
				nc, ok := rep.(docsbot.NeedCategory)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-pending"), nc.Doc.ID)
				require.Len(t, repo.docs, 1, "no new row")
			},
		},
		{
			name:   "saved duplicate reports already saved",
			seed:   &saved,
			unique: "dup-saved",
			assert: func(t *testing.T, rep docsbot.Reply, _ *fakeRepo) {
				as, ok := rep.(docsbot.AlreadySaved)
				require.True(t, ok)
				require.Equal(t, docsbot.DocumentID("d-saved"), as.Doc.ID)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			if tt.seed != nil {
				repo.seed(*tt.seed)
			}
			svc := docsbot.NewService(repo, fakeDrive{})
			rep, err := svc.Receive(t.Context(), incoming(tt.unique))
			require.NoError(t, err)
			tt.assert(t, rep, repo)
		})
	}
}

func TestService_ChooseCategory(t *testing.T) {
	tests := []struct {
		name          string
		savedBefore   int // extra saved docs already present
		connected     bool
		cat           docsbot.Category
		wantErr       error
		wantOffer     bool
		wantBackupPed bool
	}{
		{name: "first document offers Drive", cat: docsbot.CatPassport, wantOffer: true},
		{name: "second document does not offer", savedBefore: 1, cat: docsbot.CatPassport},
		{name: "fifth document offers Drive", savedBefore: 4, cat: docsbot.CatAuto, wantOffer: true},
		{name: "connected queues backup, never offers", connected: true, cat: docsbot.CatPassport, wantBackupPed: true},
		{name: "unknown category rejected", cat: docsbot.Category("bogus"), wantErr: docsbot.ErrUnknownCategory},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			for i := 0; i < tt.savedBefore; i++ {
				repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
			}
			doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusPending, Title: "Паспорт", TgFileUniqueID: "u"})
			svc := docsbot.NewService(repo, fakeDrive{connected: tt.connected})

			got, err := svc.ChooseCategory(t.Context(), user, doc.ID, tt.cat)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantOffer, got.OfferDrive)
			require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
			require.Equal(t, tt.cat, repo.docs[doc.ID].Category)
			if tt.wantBackupPed {
				require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
			} else {
				require.Equal(t, docsbot.BackupNone, repo.docs[doc.ID].BackupStatus)
			}
		})
	}
}

func TestService_ChooseCategory_idempotent(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, TgFileUniqueID: "u",
	})
	svc := docsbot.NewService(repo, fakeDrive{})

	got, err := svc.ChooseCategory(t.Context(), user, doc.ID, docsbot.CatReceipts)
	require.NoError(t, err)
	require.False(t, got.OfferDrive)
	require.Equal(t, docsbot.CatPassport, repo.docs[doc.ID].Category, "a second tap must not change the category")
}

func TestService_Rename(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Title: "old"})
	svc := docsbot.NewService(repo, fakeDrive{})

	_, err := svc.Rename(t.Context(), user, doc.ID, "   ")
	require.ErrorIs(t, err, docsbot.ErrEmptyTitle)

	card, err := svc.Rename(t.Context(), user, doc.ID, "  Новый паспорт  ")
	require.NoError(t, err)
	require.Equal(t, "Новый паспорт", card.Doc.Title)
}

func TestService_Recategorize_rejectsUnknown(t *testing.T) {
	repo := newFakeRepo()
	doc := repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther})
	svc := docsbot.NewService(repo, fakeDrive{})

	_, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.Category("nope"))
	require.ErrorIs(t, err, docsbot.ErrUnknownCategory)
}

func TestService_CategoryPage_paginates(t *testing.T) {
	repo := newFakeRepo()
	for i := 0; i < 25; i++ {
		repo.seed(docsbot.Document{UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatReceipts})
	}
	svc := docsbot.NewService(repo, fakeDrive{})

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
	svc := docsbot.NewService(repo, fakeDrive{})

	found, err := svc.Search(t.Context(), user, "паспорт")
	require.NoError(t, err)
	require.Len(t, found.Docs, 1)
	require.Equal(t, "Паспорт РК", found.Docs[0].Title)
}
