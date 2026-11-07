//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// savedDoc creates a saved document for user.
func savedDoc(t *testing.T, repo docsbot.Repo, user docsbot.UserID, unique string) docsbot.Document {
	t.Helper()
	ctx := t.Context()
	doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, Title: "t", FileID: "f-" + unique, FileUniqueID: unique,
	})
	require.NoError(t, err)
	require.NoError(t, repo.SaveDocument(ctx, user, doc.ID, docsbot.CatOther))
	return doc
}

func TestBackups_markAndList(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	a := savedDoc(t, repo, user, "a")
	b := savedDoc(t, repo, user, "b")

	ids, err := repo.NotBackedUpIDs(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{a.ID, b.ID}, ids)

	require.NoError(t, repo.MarkBackedUp(ctx, a.ID, "drive-a"))
	got, err := repo.DocumentByID(ctx, user, a.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupBackedUp, got.BackupStatus)
	require.Equal(t, "drive-a", got.DriveFileID)

	ids, err = repo.NotBackedUpIDs(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{b.ID}, ids)
}

func TestBackups_excludesTooLarge(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()

	large := savedDoc(t, repo, user, "large")
	failed := savedDoc(t, repo, user, "failed")
	require.NoError(t, repo.SetBackupStatus(ctx, large.ID, docsbot.BackupTooLarge))
	require.NoError(t, repo.SetBackupStatus(ctx, failed.ID, docsbot.BackupFailed))

	ids, err := repo.NotBackedUpIDs(ctx, user)
	require.NoError(t, err)
	require.Equal(t, []docsbot.DocumentID{failed.ID}, ids)

	n, err := repo.CountNotBackedUp(ctx, user)
	require.NoError(t, err)
	require.Equal(t, 1, n)
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

func TestBackups_enqueueUploadTx(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	doc := savedDoc(t, repo, user, "tx")

	boom := errors.New("insert failed")
	err := repo.EnqueueUpload(ctx, doc.ID, func(context.Context, pgx.Tx) error { return boom })
	require.ErrorIs(t, err, boom)
	got, err := repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupNone, got.BackupStatus, "rolled back")

	require.NoError(t, repo.EnqueueUpload(ctx, doc.ID, func(context.Context, pgx.Tx) error { return nil }))
	got, err = repo.DocumentByID(ctx, user, doc.ID)
	require.NoError(t, err)
	require.Equal(t, docsbot.BackupPending, got.BackupStatus)
}
