//go:build integration

package postgres_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// domainRepo exercises *adapters through the docsbot.Repo port.
func domainRepo(t *testing.T) (*adapters, docsbot.UserID) {
	t.Helper()
	return domainRepoWith(t, noJobs{})
}

// domainRepoWith is domainRepo with a chosen job inserter.
func domainRepoWith(t *testing.T, jobs postgres.JobInserter) (*adapters, docsbot.UserID) {
	t.Helper()
	repo := newAdapters(dbtest.New(t), jobs)
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
	mustSave(t, repo, user, doc.ID, docsbot.CatPassport)
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
	_, err = repo.SetTitle(ctx, user, doc.ID, "Паспорт РК", false)
	require.NoError(t, err)
	_, err = repo.SetCategory(ctx, user, doc.ID, docsbot.CatOther, false)
	require.NoError(t, err)
	moved, err := repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, "Паспорт РК", moved.Title)
	require.Equal(t, docsbot.CatOther, moved.Category)

	// Mark backed up: it drops out of the not-backed-up count.
	require.NoError(t, repo.SetBackupStatus(ctx, user, doc.ID, docsbot.BackupBackedUp))
	notBacked, err = repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 0, notBacked)

	_, err = repo.DeleteDocument(ctx, user, doc.ID, false)
	require.NoError(t, err)
	_, err = repo.DocumentByID(ctx, user, doc.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestDocuments_sizeAndMIME(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	withMeta, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "a.pdf", FileID: "f1", FileUniqueID: "u1",
		FileSize: 12345, MIME: "application/pdf",
	})
	require.NoError(t, err)
	got, err := repo.DocumentByID(ctx, user, withMeta.ID)
	require.NoError(t, err)
	require.Equal(t, int64(12345), got.FileSize)
	require.Equal(t, "application/pdf", got.MIME)

	bare, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindPhoto, Title: "", FileID: "f2", FileUniqueID: "u2",
	})
	require.NoError(t, err)
	got, err = repo.DocumentByID(ctx, user, bare.ID)
	require.NoError(t, err)
	require.Zero(t, got.FileSize)
	require.Empty(t, got.MIME)
}

func TestDocuments_titleExistsInCategory(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "Медицина — 08.11.2026", FileID: "f", FileUniqueID: "u",
	})
	require.NoError(t, err)

	// Pending documents do not count.
	ok, err := repo.TitleExistsInCategory(ctx, user, docsbot.CatMedical, "Медицина — 08.11.2026")
	require.NoError(t, err)
	require.False(t, ok)

	mustSave(t, repo, user, d.ID, docsbot.CatMedical)

	tests := []struct {
		name  string
		cat   docsbot.Category
		title string
		want  bool
	}{
		{name: "same category and title", cat: docsbot.CatMedical, title: "Медицина — 08.11.2026", want: true},
		{name: "other title", cat: docsbot.CatMedical, title: "Медицина — 09.11.2026"},
		{name: "other category", cat: docsbot.CatOther, title: "Медицина — 08.11.2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.TitleExistsInCategory(ctx, user, tt.cat, tt.title)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDocuments_listPagination(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	for i := 0; i < 25; i++ {
		d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument, Title: "Чек",
			FileID: "f", FileUniqueID: "u-" + strconv.Itoa(i),
		})
		require.NoError(t, err)
		mustSave(t, repo, user, d.ID, docsbot.CatReceipts)
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
	mustSave(t, repo, user, d.ID, docsbot.CatPassport)

	for _, q := range []string{"паспорт", "Паспорт", "паспрт"} {
		got, err := repo.Search(ctx, user, q, 10)
		require.NoError(t, err)
		require.Len(t, got, 1, "query %q should find the document", q)
		require.Equal(t, d.ID, got[0].ID)
	}
}

func TestDocuments_resetPendingUploads(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	mk := func(n int, s docsbot.BackupStatus) docsbot.DocumentID {
		doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument, Title: "d",
			FileID: "f" + strconv.Itoa(n), FileUniqueID: "u" + strconv.Itoa(n),
		})
		require.NoError(t, err)
		mustSave(t, repo, user, doc.ID, docsbot.CatOther)
		require.NoError(t, repo.SetBackupStatus(ctx, user, doc.ID, s))
		return doc.ID
	}
	statuses := []docsbot.BackupStatus{
		docsbot.BackupPending, docsbot.BackupUploading, docsbot.BackupBackedUp,
		docsbot.BackupTooLarge, docsbot.BackupFailed, docsbot.BackupNone,
	}
	ids := make([]docsbot.DocumentID, len(statuses))
	for i, s := range statuses {
		ids[i] = mk(i, s)
	}

	require.NoError(t, repo.ResetPendingUploads(ctx, user))

	want := []docsbot.BackupStatus{
		docsbot.BackupNone, docsbot.BackupNone, docsbot.BackupBackedUp,
		docsbot.BackupTooLarge, docsbot.BackupFailed, docsbot.BackupNone,
	}
	for i, id := range ids {
		got, err := repo.DocumentByID(ctx, user, id)
		require.NoError(t, err)
		require.Equal(t, want[i], got.BackupStatus, "from %s", statuses[i])
	}
}

func TestDocuments_markUploadingKeepsUpdatedAt(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	user := docsbot.UserID(makeUser(t, repo, 700).ID.String())
	ctx := t.Context()

	doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "d", FileID: "f", FileUniqueID: "u",
	})
	require.NoError(t, err)
	mustSave(t, repo, user, doc.ID, docsbot.CatOther)

	old := time.Now().Add(-25 * time.Hour).UTC().Truncate(time.Microsecond)
	_, err = pool.Exec(ctx, `update documents set backup_status = 'pending', updated_at = $2 where id = $1::uuid`, string(doc.ID), old)
	require.NoError(t, err)

	state := func() (string, time.Time) {
		var status string
		var at time.Time
		require.NoError(t, pool.QueryRow(ctx, `select backup_status, updated_at from documents where id = $1::uuid`, string(doc.ID)).Scan(&status, &at))
		return status, at
	}

	require.NoError(t, repo.MarkUploading(ctx, user, doc.ID))
	status, at := state()
	require.Equal(t, "uploading", status)
	require.True(t, old.Equal(at), "updated_at must not change, got %s", at)

	require.NoError(t, repo.SetBackupStatus(ctx, user, doc.ID, docsbot.BackupFailed))
	status, at = state()
	require.Equal(t, "failed", status)
	require.True(t, at.After(old), "SetBackupStatus must bump updated_at")
}

func TestDocuments_pageCount(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	load := func(id docsbot.DocumentID) int {
		t.Helper()
		d, err := repo.DocumentByID(ctx, user, id)
		require.NoError(t, err)
		return d.PageCount
	}

	known, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "a", FileID: "f1", FileUniqueID: "u1", MIME: "application/pdf", PageCount: 3,
		Width: 1280, Height: 960,
	})
	require.NoError(t, err)
	require.Equal(t, 3, known.PageCount)
	require.Equal(t, [2]int{1280, 960}, [2]int{known.Width, known.Height})
	require.Equal(t, 3, load(known.ID))

	unknown, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: user, Kind: docsbot.KindDocument, Title: "b", FileID: "f2", FileUniqueID: "u2"})
	require.NoError(t, err)
	require.Zero(t, load(unknown.ID), "0 is stored as null and reads back as unknown")
	got, err := repo.DocumentByID(ctx, user, unknown.ID)
	require.NoError(t, err)
	require.Zero(t, got.Width)
	require.Zero(t, got.Height)
}

// A row saved under a retired slug (before the category set changed) is "Другое":
// listed there, counted there, and in no other category.
func TestDocuments_retiredSlugBelongsToOther(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	mk := func(unique, title string, cat docsbot.Category) docsbot.DocumentID {
		t.Helper()
		d, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: user, Kind: docsbot.KindDocument, Title: title, FileID: "f" + unique, FileUniqueID: unique})
		require.NoError(t, err)
		saved, err := repo.SaveChosen(ctx, docsbot.SaveChoice{User: user, ID: d.ID, Category: cat, Backup: docsbot.BackupNone})
		require.NoError(t, err)
		require.True(t, saved)
		return d.ID
	}
	retired := mk("r1", "Старый договор", "contracts")
	retired2 := mk("r2", "Старая работа", "edu_work")
	other := mk("o1", "Просто другое", docsbot.CatOther)
	passport := mk("p1", "Паспорт", docsbot.CatPassport)

	ids := func(cat docsbot.Category) []docsbot.DocumentID {
		t.Helper()
		docs, err := repo.ListByCategory(ctx, user, cat, 50, 0)
		require.NoError(t, err)
		var out []docsbot.DocumentID
		for _, d := range docs {
			out = append(out, d.ID)
		}
		return out
	}
	require.ElementsMatch(t, []docsbot.DocumentID{retired, retired2, other}, ids(docsbot.CatOther))
	require.ElementsMatch(t, []docsbot.DocumentID{passport}, ids(docsbot.CatPassport))
	for _, c := range docsbot.AllCategories {
		if c == docsbot.CatOther || c == docsbot.CatPassport {
			continue
		}
		require.Empty(t, ids(c), string(c))
	}
	counts, err := repo.CategoryCounts(ctx, user)
	require.NoError(t, err)
	got := map[docsbot.Category]int{}
	for _, c := range counts {
		got[c.Category] += c.Count
	}
	require.Equal(t, 1, got[docsbot.CatPassport])
	require.Equal(t, 1, got["contracts"], "the raw count keeps the slug; the service folds it into other")
	require.Equal(t, 1, got["edu_work"])
	require.Equal(t, 1, got[docsbot.CatOther])
}

func TestSearch_treatsMetacharactersLiterally(t *testing.T) {
	repo, user, _ := twoUsers(t)
	ctx := t.Context()
	percent := titled(t, repo, user, "p", "Скидка 100% навсегда")
	under := titled(t, repo, user, "u", "file_name final")
	plain := titled(t, repo, user, "x", "fileXname final")
	slash := titled(t, repo, user, "s", `C:\scans\passport`)
	emoji := titled(t, repo, user, "e", "Паспорт 🛂 РК")
	cyr := titled(t, repo, user, "c", "Договор аренды")

	ids := func(q string) []docsbot.DocumentID {
		t.Helper()
		docs, err := repo.Search(ctx, user, q, 10)
		require.NoError(t, err, "query %q must not fail", q)
		out := make([]docsbot.DocumentID, 0, len(docs))
		for _, d := range docs {
			out = append(out, d.ID)
		}
		return out
	}

	require.Equal(t, []docsbot.DocumentID{percent.ID}, ids("%"), "% is a literal, not a wildcard for everything")
	require.Equal(t, []docsbot.DocumentID{percent.ID}, ids("100%"))
	require.Contains(t, ids("_name"), under.ID)
	require.NotContains(t, ids("_name"), plain.ID, "_ must not match any single character")
	require.Equal(t, []docsbot.DocumentID{slash.ID}, ids(`\`), "a lone backslash neither fails nor matches everything")
	require.Equal(t, []docsbot.DocumentID{slash.ID}, ids(`\scans\`))
	require.Contains(t, ids("🛂"), emoji.ID)
	require.Contains(t, ids("ДОГОВОР"), cyr.ID, "Cyrillic is case-insensitive")
	require.Contains(t, ids("договор"), cyr.ID)
	require.Empty(t, ids(strings.Repeat("я", 10_000)), "a huge query is harmless")
}

func TestSearch_otherUsersDocumentsAreInvisible(t *testing.T) {
	repo, a, b := twoUsers(t)
	titled(t, repo, a, "a1", "Секрет")

	docs, err := repo.Search(t.Context(), b, "%", 10)
	require.NoError(t, err)
	require.Empty(t, docs)
}

func TestSetTitleCategoryDelete_missingDocumentIsNotFound(t *testing.T) {
	repo, a, _ := twoUsers(t)
	ctx := t.Context()
	const missing docsbot.DocumentID = "00000000-0000-7000-8000-000000000000"

	_, err := repo.SetTitle(ctx, a, missing, "x", false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = repo.SetCategory(ctx, a, missing, docsbot.CatOther, false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = repo.DeleteDocument(ctx, a, missing, false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)

	// Double delete: the second sees it gone.
	doc := titled(t, repo, a, "dd", "d")
	_, err = repo.DeleteDocument(ctx, a, doc.ID, false)
	require.NoError(t, err)
	_, err = repo.DeleteDocument(ctx, a, doc.ID, false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}
