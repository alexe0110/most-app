//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
