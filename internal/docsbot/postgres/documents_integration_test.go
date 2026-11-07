//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// domainRepo exercises *postgres.Repo through the docsbot.Repo port.
func domainRepo(t *testing.T) (*postgres.Repo, docsbot.UserID) {
	t.Helper()
	repo := postgres.NewRepo(dbtest.New(t))
	u := makeUser(t, repo, 700)
	return repo, docsbot.UserID(u.ID.String())
}

func TestDocuments_lifecycle(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "Паспорт",
		FileID: "file-1", FileUniqueID: "uniq-1",
	})
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusPending, doc.Status)
	require.Equal(t, docsbot.BackupNone, doc.BackupStatus)
	require.NotEmpty(t, doc.ID)

	// Same (user, file_unique_id) is rejected.
	_, err = repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "дубль",
		FileID: "file-2", FileUniqueID: "uniq-1",
	})
	require.Error(t, err)

	got, err := repo.DocumentByFileUniqueID(ctx, user, "uniq-1")
	require.NoError(t, err)
	require.Equal(t, doc.ID, got.ID)

	// Save with a category, then it shows up in the catalog.
	require.NoError(t, repo.SaveDocument(ctx, user, doc.ID, docsbot.CatPassport))
	saved, err := repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.StatusSaved, saved.Status)
	require.Equal(t, docsbot.CatPassport, saved.Category)

	n, err := repo.CountSaved(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	notBacked, err := repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, notBacked)

	counts, err := repo.CategoryCounts(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.CategoryCount{{Category: docsbot.CatPassport, Count: 1}}, counts)

	// Rename and recategorize.
	require.NoError(t, repo.SetTitle(ctx, user, doc.ID, "Паспорт РК"))
	require.NoError(t, repo.SetCategory(ctx, user, doc.ID, docsbot.CatOther))
	moved, err := repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, "Паспорт РК", moved.Title)
	require.Equal(t, docsbot.CatOther, moved.Category)

	// Mark backed up: it drops out of the not-backed-up count.
	require.NoError(t, repo.SetBackupStatus(ctx, doc.ID, docsbot.BackupBackedUp))
	notBacked, err = repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 0, notBacked)

	require.NoError(t, repo.DeleteDocument(ctx, user, doc.ID))
	_, err = repo.DocumentByID(ctx, user, doc.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestDocuments_listPagination(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	for i := 0; i < 25; i++ {
		d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument, Title: "Чек",
			FileID: "f", FileUniqueID: "u-" + itoa(i),
		})
		require.NoError(t, err)
		require.NoError(t, repo.SaveDocument(ctx, user, d.ID, docsbot.CatReceipts))
	}

	first, err := repo.ListByCategory(ctx, user, docsbot.CatReceipts, 10, 0)
	require.NoError(t, err)
	require.Len(t, first, 10)

	last, err := repo.ListByCategory(ctx, user, docsbot.CatReceipts, 10, 20)
	require.NoError(t, err)
	require.Len(t, last, 5)
}

func TestDocuments_searchTypoTolerant(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "Паспорт",
		FileID: "f", FileUniqueID: "u",
	})
	require.NoError(t, err)
	require.NoError(t, repo.SaveDocument(ctx, user, d.ID, docsbot.CatPassport))

	for _, q := range []string{"паспорт", "Паспорт", "паспрт"} {
		got, err := repo.Search(ctx, user, q, 10)
		require.NoError(t, err)
		require.Len(t, got, 1, "query %q should find the document", q)
		require.Equal(t, d.ID, got[0].ID)
	}
}

func TestDocuments_scopedByUser(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	other := docsbot.UserID(makeUser(t, repo, 701).ID.String())

	d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "Паспорт", FileID: "f", FileUniqueID: "u",
	})
	require.NoError(t, err)

	_, err = repo.DocumentByID(ctx, other, d.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound, "one user cannot read another's document")
}

// itoa avoids importing strconv for a tiny loop counter.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
