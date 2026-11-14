//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
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
