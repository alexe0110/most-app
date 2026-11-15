//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// savedDoc creates a saved document for user.
func savedDoc(t *testing.T, repo *adapters, user docsbot.UserID, unique string) docsbot.Document {
	t.Helper()
	doc, err := repo.CreateDocument(t.Context(), docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "t", FileID: "f-" + unique, FileUniqueID: unique,
	})
	require.NoError(t, err)
	mustSave(t, repo, user, doc.ID, docsbot.CatOther)
	return doc
}

func TestBackups_markAndList(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	a := savedDoc(t, repo, user, "a")
	b := savedDoc(t, repo, user, "b")

	_, err := repo.MarkBackedUp(ctx, user, a.ID, "drive-a", 0, 0)
	require.NoError(t, err)
	got, err := repo.DocumentByID(ctx, user, a.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupBackedUp, got.BackupStatus)
	require.Equal(t, "drive-a", got.DriveFileID)

	ids, err := repo.ClaimTransferable(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{b.ID}, ids, "the backed-up document is not claimed")
}

func TestBackups_excludesTooLarge(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	large := savedDoc(t, repo, user, "large")
	failed := savedDoc(t, repo, user, "failed")
	require.NoError(t, repo.SetBackupStatus(ctx, user, large.ID, docsbot.BackupTooLarge))
	require.NoError(t, repo.SetBackupStatus(ctx, user, failed.ID, docsbot.BackupFailed))

	n, err := repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	ids, err := repo.ClaimTransferable(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{failed.ID}, ids)
}

// The sweeper only recovers uploads that are old, still in flight and belong to
// a user whose Drive is connected; anything else it must leave alone.
func TestBackups_stuckUploadsListsOnlyOldInFlightOfActiveIntegrations(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	ctx := t.Context()
	seed := func(tgID int64, integration, backup, age string) docsbot.DocumentID {
		t.Helper()
		user := docsbot.UserID(makeUser(t, repo, tgID).ID.String())
		if integration != "" {
			_, err := pool.Exec(ctx, `insert into integrations (user_id, bot, provider, status) values ($1::uuid, 'docs', 'google', $2)`, string(user), integration)
			require.NoError(t, err)
		}
		doc := savedDoc(t, repo, user, strconv.FormatInt(tgID, 10))
		_, err := pool.Exec(ctx, `update documents set backup_status = $2, updated_at = now() - $3::interval where id = $1::uuid`, string(doc.ID), backup, age)
		require.NoError(t, err)
		return doc.ID
	}
	pending := seed(940, "active", "pending", "31 minutes")
	uploading := seed(941, "active", "uploading", "2 hours")
	seed(942, "active", "uploading", "1 minute")
	seed(943, "active", "backed_up", "2 hours")
	seed(944, "active", "none", "2 hours")
	seed(945, "revoked", "uploading", "2 hours")
	seed(946, "", "uploading", "2 hours")
	olderThan := time.Now().Add(-30 * time.Minute)

	got, err := repo.StuckUploads(ctx, olderThan, 10)

	require.NoError(t, err)
	ids := make([]docsbot.DocumentID, 0, len(got))
	for _, s := range got {
		ids = append(ids, s.ID)
	}
	require.Equal(t, []docsbot.DocumentID{uploading, pending}, ids, "oldest first")

	limited, err := repo.StuckUploads(ctx, olderThan, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, uploading, limited[0].ID)
}

func TestBackups_folderCache(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	uid := string(user)

	_, ok, err := repo.FolderID(ctx, uid, "passport")
	require.NoError(t, err)
	require.False(t, ok, "missing key")

	require.NoError(t, repo.SaveFolderID(ctx, uid, "passport", "folder-1"))
	id, ok, err := repo.FolderID(ctx, uid, "passport")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "folder-1", id)

	require.NoError(t, repo.SaveFolderID(ctx, uid, "passport", "folder-2"))
	id, _, err = repo.FolderID(ctx, uid, "passport")
	require.NoError(t, err)
	require.Equal(t, "folder-2", id, "save replaces")

	require.NoError(t, repo.DeleteFolderID(ctx, uid, "passport"))
	_, ok, err = repo.FolderID(ctx, uid, "passport")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestBackups_deleteAllFolders(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	other := makeUser(t, repo, 701).ID.String()

	for _, key := range []string{"passport", "insurance"} {
		require.NoError(t, repo.SaveFolderID(ctx, string(user), key, "f-"+key))
	}
	require.NoError(t, repo.SaveFolderID(ctx, other, "passport", "other-f"))

	require.NoError(t, repo.DeleteAllFolders(ctx, user))

	for _, key := range []string{"passport", "insurance"} {
		_, ok, err := repo.FolderID(ctx, string(user), key)
		require.NoError(t, err)
		require.False(t, ok, key)
	}
	id, ok, err := repo.FolderID(ctx, other, "passport")
	require.NoError(t, err)
	require.True(t, ok, "other user's cache is untouched")
	require.Equal(t, "other-f", id)
}

func TestBackups_saveChosenTx(t *testing.T) {
	tests := []struct {
		name       string
		jobs       postgres.JobInserter
		wantErr    error
		wantStatus docsbot.Status
		wantBackup docsbot.BackupStatus
	}{
		{"insert fails: nothing is saved", uploadJobs{err: errBoom}, errBoom, docsbot.StatusPending, docsbot.BackupNone},
		{"live job skipped: the choice still commits", uploadJobs{skipped: true}, nil, docsbot.StatusSaved, docsbot.BackupPending},
		{"job queued", uploadJobs{}, nil, docsbot.StatusSaved, docsbot.BackupPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, user := domainRepoWith(t, tt.jobs)
			ctx := t.Context()
			doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{UserID: user, Kind: docsbot.KindDocument, Title: "t", FileID: "f", FileUniqueID: "u"})
			require.NoError(t, err)

			_, err = repo.SaveChosen(ctx, docsbot.SaveChoice{User: user, ID: doc.ID, Category: docsbot.CatOther, Backup: docsbot.BackupPending})

			require.ErrorIs(t, err, tt.wantErr)
			got, err := repo.DocumentByID(ctx, user, doc.ID)
			require.NoError(t, err)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantBackup, got.BackupStatus)
		})
	}
}

func TestFolders_categoryFolderID(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	require.NoError(t, repo.SaveFolderID(ctx, string(user), "passport", "folder-x"))

	id, ok, err := repo.CategoryFolderID(ctx, user, docsbot.Category("passport"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "folder-x", id)

	id, ok, err = repo.CategoryFolderID(ctx, user, docsbot.Category("medical"))
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, id)
}

// noJobs is a JobInserter for tests where no job may be needed.
type noJobs struct{}

var (
	errUnexpectedJob = errors.New("unexpected job insert")
	errBoom          = errors.New("insert failed")
)

var _ postgres.JobInserter = noJobs{}

func (noJobs) InsertUpload(context.Context, pgx.Tx, docsbot.UserID, docsbot.DocumentID) (bool, error) {
	return false, errUnexpectedJob
}

func (noJobs) InsertSync(context.Context, pgx.Tx, docsbot.UserID, docsbot.DocumentID) error {
	return errUnexpectedJob
}

func (noJobs) InsertTrash(context.Context, pgx.Tx, docsbot.UserID, string) error {
	return errUnexpectedJob
}

// uploadJobs is a JobInserter whose upload insert returns a fixed result.
type uploadJobs struct {
	noJobs
	skipped bool
	err     error
}

func (u uploadJobs) InsertUpload(context.Context, pgx.Tx, docsbot.UserID, docsbot.DocumentID) (bool, error) {
	return u.skipped, u.err
}

// The reason of a failed backup is stored with the status and cleared whenever
// the document leaves the failed state (any other status, retry claims).
func TestBackups_failReason(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	reason := func(id docsbot.DocumentID) docsbot.BackupReason {
		t.Helper()
		d, err := repo.DocumentByID(ctx, user, id)
		require.NoError(t, err)
		return d.BackupReason
	}

	doc := savedDoc(t, repo, user, "quota")
	require.NoError(t, repo.FailBackup(ctx, user, doc.ID, docsbot.BackupReasonQuota))
	got, err := repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupFailed, got.BackupStatus)
	require.Equal(t, docsbot.BackupReasonQuota, got.BackupReason)

	claimed, err := repo.ClaimFailed(ctx, user, doc.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Empty(t, reason(doc.ID), "a retry clears the reason")

	require.NoError(t, repo.FailBackup(ctx, user, doc.ID, docsbot.BackupReasonRevoked))
	require.NoError(t, repo.SetBackupStatus(ctx, user, doc.ID, docsbot.BackupBackedUp))
	require.Empty(t, reason(doc.ID), "another status clears the reason")

	other := savedDoc(t, repo, user, "bulk")
	require.NoError(t, repo.FailBackup(ctx, user, other.ID, docsbot.BackupReasonUnavailable))
	ids, err := repo.ClaimTransferable(ctx, user)
	require.NoError(t, err)
	require.Contains(t, ids, other.ID)
	require.Empty(t, reason(other.ID), "a bulk claim clears the reason")

	// A worker that was already running when the sweeper failed its document
	// still finishes: success and the uploading claim must not keep a stale reason.
	late := savedDoc(t, repo, user, "late")
	require.NoError(t, repo.FailBackup(ctx, user, late.ID, docsbot.BackupReasonUnavailable))
	require.NoError(t, repo.MarkUploading(ctx, user, late.ID))
	require.Empty(t, reason(late.ID), "uploading clears the reason")
	require.NoError(t, repo.FailBackup(ctx, user, late.ID, docsbot.BackupReasonUnavailable))
	_, err = repo.MarkBackedUp(ctx, user, late.ID, "drive-late", 0, 0)
	require.NoError(t, err)
	require.Empty(t, reason(late.ID), "a successful upload clears the reason")

	foreign := docsbot.UserID("00000000-0000-0000-0000-000000000000")
	require.NoError(t, repo.FailBackup(ctx, foreign, doc.ID, docsbot.BackupReasonQuota))
	require.Empty(t, reason(doc.ID), "a foreign user cannot fail the document")
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
