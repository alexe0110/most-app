//go:build integration

package postgres_test

import (
	"testing"
	"time"

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
	mustSave(t, ctx, repo, user, doc.ID, docsbot.CatPassport)
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
	require.NoError(t, repo.SetBackupStatus(ctx, user, doc.ID, docsbot.BackupBackedUp))
	notBacked, err = repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 0, notBacked)

	require.NoError(t, repo.DeleteDocument(ctx, user, doc.ID))
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

	mustSave(t, ctx, repo, user, d.ID, docsbot.CatMedical)

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
			FileID: "f", FileUniqueID: "u-" + itoa(i),
		})
		require.NoError(t, err)
		mustSave(t, ctx, repo, user, d.ID, docsbot.CatReceipts)
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
	mustSave(t, ctx, repo, user, d.ID, docsbot.CatPassport)

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

func TestDocuments_resetPendingUploads(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	mk := func(n int, s docsbot.BackupStatus) docsbot.DocumentID {
		doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument, Title: "d",
			FileID: "f" + itoa(n), FileUniqueID: "u" + itoa(n),
		})
		require.NoError(t, err)
		mustSave(t, ctx, repo, user, doc.ID, docsbot.CatOther)
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
	repo := postgres.NewRepo(pool)
	user := docsbot.UserID(makeUser(t, repo, 700).ID.String())
	ctx := t.Context()

	doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "d", FileID: "f", FileUniqueID: "u",
	})
	require.NoError(t, err)
	mustSave(t, ctx, repo, user, doc.ID, docsbot.CatOther)

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

func TestSyncs_pageCountOnBackedUpAndReplace(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()

	// MarkBackedUp records pages when known and keeps the stored value otherwise.
	id := e.seed(t, e.user, "pg1", "uploading", "ok")
	_, err := e.repo.MarkBackedUpForSync(ctx, e.user, id, "drive-1", 0, 5, noInsert)
	require.NoError(t, err)
	require.Equal(t, 5, e.pages(t, id))

	id2 := e.seed(t, e.user, "pg2", "uploading", "ok")
	_, err = e.repo.MarkBackedUpForSync(ctx, e.user, id2, "drive-2", 0, 0, noInsert)
	require.NoError(t, err)
	require.Zero(t, e.pages(t, id2), "unknown stays unknown")

	// coalesce: an unknown count never wipes a known one.
	id3 := e.seed(t, e.user, "pg3", "uploading", "ok")
	_, err = e.pool.Exec(ctx, `update documents set page_count = 9 where id = $1::uuid`, string(id3))
	require.NoError(t, err)
	_, err = e.repo.MarkBackedUpForSync(ctx, e.user, id3, "drive-3", 0, 0, noInsert)
	require.NoError(t, err)
	require.Equal(t, 9, e.pages(t, id3))

	// ReplaceContent swaps the file, so its page count follows the new file, known or not.
	id4 := e.seed(t, e.user, "pg4", "none", "ok")
	_, err = e.pool.Exec(ctx, `update documents set page_count = 9 where id = $1::uuid`, string(id4))
	require.NoError(t, err)
	in := docsbot.IncomingFile{Kind: docsbot.KindDocument, FileID: "n1", FileUniqueID: "n1u", MIME: "application/pdf", PageCount: 2}
	replaced, err := e.repo.ReplaceContentForSync(ctx, e.user, id4, in, false, noInsert)
	require.NoError(t, err)
	require.True(t, replaced)
	require.Equal(t, 2, e.pages(t, id4))
	_, err = e.pool.Exec(ctx, `update documents set image_width = 800, image_height = 600 where id = $1::uuid`, string(id4))
	require.NoError(t, err)
	in = docsbot.IncomingFile{Kind: docsbot.KindPhoto, FileID: "n2", FileUniqueID: "n2u"}
	replaced, err = e.repo.ReplaceContentForSync(ctx, e.user, id4, in, false, noInsert)
	require.NoError(t, err)
	require.True(t, replaced)
	require.Zero(t, e.pages(t, id4), "a file of unknown length does not keep the old file's pages")
	var w, h *int
	require.NoError(t, e.pool.QueryRow(ctx, `select image_width, image_height from documents where id = $1::uuid`, string(id4)).Scan(&w, &h))
	require.Nil(t, w, "a replaced file never shows the old pixel size")
	require.Nil(t, h)
}

// pages reads the stored page count (0 for null).
func (e syncEnv) pages(t *testing.T, id docsbot.DocumentID) int {
	t.Helper()
	var n *int
	require.NoError(t, e.pool.QueryRow(t.Context(), `select page_count from documents where id = $1::uuid`, string(id)).Scan(&n))
	if n == nil {
		return 0
	}
	return *n
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
		saved, err := repo.SaveDocument(ctx, user, d.ID, cat)
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
