//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
)

// twoUsers returns a repo with users A and B.
func twoUsers(t *testing.T) (repo *postgres.Repo, a, b docsbot.UserID) {
	t.Helper()
	repo = newRepo(t)
	return repo, docsbot.UserID(makeUser(t, repo, 900).ID.String()), docsbot.UserID(makeUser(t, repo, 901).ID.String())
}

func titled(t *testing.T, repo *postgres.Repo, user docsbot.UserID, unique, title string) docsbot.Document {
	t.Helper()
	doc, err := repo.CreateDocument(t.Context(), docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: title, FileID: "f-" + unique, FileUniqueID: unique,
	})
	require.NoError(t, err)
	mustSave(t, t.Context(), repo, user, doc.ID, docsbot.CatOther)
	return doc
}

func backupOf(t *testing.T, repo *postgres.Repo, user docsbot.UserID, id docsbot.DocumentID) docsbot.Document {
	t.Helper()
	d, err := repo.DocumentByID(t.Context(), user, id)
	require.NoError(t, err)
	return d
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

func TestClaimFailed(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()

	mk := func(unique string, st docsbot.BackupStatus) docsbot.DocumentID {
		d := titled(t, repo, a, unique, "d")
		require.NoError(t, repo.SetBackupStatus(ctx, a, d.ID, st))
		return d.ID
	}
	failed := mk("failed", docsbot.BackupFailed)

	// Someone else cannot claim it.
	ok, err := repo.ClaimFailed(ctx, b, failed)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, docsbot.BackupFailed, backupOf(t, repo, a, failed).BackupStatus)

	// The owner claims once; the double tap gets nothing.
	ok, err = repo.ClaimFailed(ctx, a, failed)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, docsbot.BackupPending, backupOf(t, repo, a, failed).BackupStatus)
	ok, err = repo.ClaimFailed(ctx, a, failed)
	require.NoError(t, err)
	require.False(t, ok)

	for _, st := range []docsbot.BackupStatus{docsbot.BackupBackedUp, docsbot.BackupUploading, docsbot.BackupPending, docsbot.BackupNone, docsbot.BackupTooLarge} {
		id := mk("s-"+string(st), st)
		ok, err = repo.ClaimFailed(ctx, a, id)
		require.NoError(t, err)
		require.False(t, ok, "%s is not retryable", st)
		require.Equal(t, st, backupOf(t, repo, a, id).BackupStatus)
	}
}

func TestClaimFailed_parallelClaimsWinOnce(t *testing.T) {
	repo, a, _ := twoUsers(t)
	d := titled(t, repo, a, "race", "d")
	require.NoError(t, repo.SetBackupStatus(t.Context(), a, d.ID, docsbot.BackupFailed))

	wins := make(chan bool, 10)
	for range 10 {
		go func() {
			ok, err := repo.ClaimFailed(t.Context(), a, d.ID)
			if err != nil {
				ok = false
			}
			wins <- ok
		}()
	}
	n := 0
	for range 10 {
		if <-wins {
			n++
		}
	}
	require.Equal(t, 1, n)
}

func TestUserScoping_otherUserCannotTouchDocument(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	doc := titled(t, repo, a, "scoped", "mine")
	require.NoError(t, repo.SetBackupStatus(ctx, a, doc.ID, docsbot.BackupUploading))

	require.NoError(t, repo.SetBackupStatus(ctx, b, doc.ID, docsbot.BackupFailed))
	require.NoError(t, repo.MarkUploading(ctx, b, doc.ID))
	require.NoError(t, repo.MarkBackedUp(ctx, b, doc.ID, "stolen"))
	got := backupOf(t, repo, a, doc.ID)
	require.Equal(t, docsbot.BackupUploading, got.BackupStatus)
	require.Empty(t, got.DriveFileID)

	require.ErrorIs(t, repo.SetTitle(ctx, b, doc.ID, "pwned"), docsbot.ErrNotFound)
	require.ErrorIs(t, repo.SetCategory(ctx, b, doc.ID, docsbot.CatPassport), docsbot.ErrNotFound)
	require.ErrorIs(t, repo.DeleteDocument(ctx, b, doc.ID), docsbot.ErrNotFound)
	got = backupOf(t, repo, a, doc.ID)
	require.Equal(t, "mine", got.Title)
	require.Equal(t, docsbot.CatOther, got.Category)
}

func TestSetTitleCategoryDelete_missingDocumentIsNotFound(t *testing.T) {
	repo, a, _ := twoUsers(t)
	ctx := t.Context()
	const missing docsbot.DocumentID = "00000000-0000-7000-8000-000000000000"

	require.ErrorIs(t, repo.SetTitle(ctx, a, missing, "x"), docsbot.ErrNotFound)
	require.ErrorIs(t, repo.SetCategory(ctx, a, missing, docsbot.CatOther), docsbot.ErrNotFound)
	require.ErrorIs(t, repo.DeleteDocument(ctx, a, missing), docsbot.ErrNotFound)

	// Double delete: the second sees it gone.
	doc := titled(t, repo, a, "dd", "d")
	require.NoError(t, repo.DeleteDocument(ctx, a, doc.ID))
	require.ErrorIs(t, repo.DeleteDocument(ctx, a, doc.ID), docsbot.ErrNotFound)
}

func TestClaimTransferable_onlyNoneAndFailedOwnedByUser(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	want := map[docsbot.BackupStatus]bool{docsbot.BackupNone: true, docsbot.BackupFailed: true}
	byStatus := map[docsbot.BackupStatus]docsbot.DocumentID{}
	for _, st := range []docsbot.BackupStatus{
		docsbot.BackupNone, docsbot.BackupFailed, docsbot.BackupPending,
		docsbot.BackupUploading, docsbot.BackupBackedUp, docsbot.BackupTooLarge,
	} {
		d := titled(t, repo, a, "n-"+string(st), "d")
		require.NoError(t, repo.SetBackupStatus(ctx, a, d.ID, st))
		byStatus[st] = d.ID
	}
	foreign := titled(t, repo, b, "foreign", "d")

	ids, err := repo.ClaimTransferable(ctx, a)
	require.NoError(t, err)
	got := map[docsbot.DocumentID]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for st, id := range byStatus {
		require.Equal(t, want[st], got[id], "status %s", st)
	}
	require.False(t, got[foreign.ID])
	require.Equal(t, docsbot.BackupNone, backupOf(t, repo, b, foreign.ID).BackupStatus, "other user's document untouched")
	for id := range got {
		require.Equal(t, docsbot.BackupPending, backupOf(t, repo, a, id).BackupStatus)
	}

	again, err := repo.ClaimTransferable(ctx, a)
	require.NoError(t, err)
	require.Empty(t, again, "a second claim finds nothing")
}

func TestClaim_parallelClaimsPartitionDocuments(t *testing.T) {
	repo, a, _ := twoUsers(t)
	const docs = 6
	for i := range docs {
		d := titled(t, repo, a, "pc-"+string(rune('a'+i)), "d")
		if i%2 == 0 {
			require.NoError(t, repo.SetBackupStatus(t.Context(), a, d.ID, docsbot.BackupFailed))
		}
	}

	type result struct {
		ids []docsbot.DocumentID
		err error
	}
	res := make(chan result, 8)
	for range 8 {
		go func() {
			ids, err := repo.ClaimTransferable(t.Context(), a)
			res <- result{ids, err}
		}()
	}
	seen := map[docsbot.DocumentID]int{}
	for range 8 {
		r := <-res
		require.NoError(t, r.err)
		for _, id := range r.ids {
			seen[id]++
		}
	}
	require.Len(t, seen, docs, "every document is claimed")
	for id, n := range seen {
		require.Equal(t, 1, n, "document %s claimed by more than one caller", id)
	}
}

func TestClaimAllFailed_onlyFailed(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	failed := titled(t, repo, a, "f", "d")
	none := titled(t, repo, a, "n", "d")
	foreign := titled(t, repo, b, "bf", "d")
	require.NoError(t, repo.SetBackupStatus(ctx, a, failed.ID, docsbot.BackupFailed))
	require.NoError(t, repo.SetBackupStatus(ctx, b, foreign.ID, docsbot.BackupFailed))

	ids, err := repo.ClaimAllFailed(ctx, a)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{failed.ID}, ids)
	require.Equal(t, docsbot.BackupNone, backupOf(t, repo, a, none.ID).BackupStatus)
	require.Equal(t, docsbot.BackupFailed, backupOf(t, repo, b, foreign.ID).BackupStatus)

	again, err := repo.ClaimAllFailed(ctx, a)
	require.NoError(t, err)
	require.Empty(t, again)
}

func TestStatusMessages_otherUserCannotTouch(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	docA := titled(t, repo, a, "sa", "d")
	docB := titled(t, repo, b, "sb", "d")
	sm, err := repo.Create(ctx, a, 900, 5, docsbot.StatusBatch)
	require.NoError(t, err)
	require.NoError(t, repo.Attach(ctx, a, docA.ID, sm.ID))

	// B cannot read it, even with the forged id (as in an onRetryBatch callback).
	_, err = repo.ByID(ctx, b, sm.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	docs, err := repo.DocumentsIn(ctx, b, sm.ID)
	require.NoError(t, err)
	require.Empty(t, docs)

	// B cannot change it.
	require.NoError(t, repo.Close(ctx, b, sm.ID))
	require.NoError(t, repo.SetMessageID(ctx, b, sm.ID, 99))
	got, err := repo.ByID(ctx, a, sm.ID)
	require.NoError(t, err)
	require.False(t, got.Closed)
	require.Equal(t, 5, got.MessageID)

	// B cannot move A's document onto B's message, nor pull its own onto A's.
	smB, err := repo.Create(ctx, b, 901, 6, docsbot.StatusBatch)
	require.NoError(t, err)
	require.NoError(t, repo.Attach(ctx, b, docA.ID, smB.ID))
	require.Equal(t, sm.ID, backupOf(t, repo, a, docA.ID).StatusMessageID, "A's document stays on A's message")
	docs, err = repo.DocumentsIn(ctx, a, sm.ID)
	require.NoError(t, err)
	require.Len(t, docs, 1)
	require.NoError(t, repo.Attach(ctx, a, docB.ID, sm.ID))
	require.Empty(t, backupOf(t, repo, b, docB.ID).StatusMessageID, "B's document is not pulled onto A's message")
}

func TestAttach_cannotUseForeignStatusMessage(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	docA := titled(t, repo, a, "own", "d")
	smB, err := repo.Create(ctx, b, 901, 7, docsbot.StatusBatch)
	require.NoError(t, err)

	require.NoError(t, repo.Attach(ctx, a, docA.ID, smB.ID))

	require.Empty(t, backupOf(t, repo, a, docA.ID).StatusMessageID, "A's own document must not join B's status message")
	docs, err := repo.DocumentsIn(ctx, b, smB.ID)
	require.NoError(t, err)
	require.Empty(t, docs)
}
