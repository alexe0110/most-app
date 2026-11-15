//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// Every query is scoped by user: user B never reads or changes user A's rows,
// even with a forged id.

func TestUserScoping_otherUserCannotTouchDocument(t *testing.T) {
	repo, a, b := twoUsers(t)
	ctx := t.Context()
	doc := titled(t, repo, a, "scoped", "mine")
	_, err := repo.DocumentByID(ctx, b, doc.ID)
	require.ErrorIs(t, err, docsbot.ErrNotFound, "one user cannot read another's document")
	require.NoError(t, repo.SetBackupStatus(ctx, a, doc.ID, docsbot.BackupUploading))

	require.NoError(t, repo.SetBackupStatus(ctx, b, doc.ID, docsbot.BackupFailed))
	require.NoError(t, repo.MarkUploading(ctx, b, doc.ID))
	_, err = repo.MarkBackedUp(ctx, b, doc.ID, "stolen", 0, 0)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	got := backupOf(t, repo, a, doc.ID)
	require.Equal(t, docsbot.BackupUploading, got.BackupStatus)
	require.Empty(t, got.DriveFileID)

	_, err = repo.SetTitle(ctx, b, doc.ID, "pwned", false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = repo.SetCategory(ctx, b, doc.ID, docsbot.CatPassport, false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = repo.DeleteDocument(ctx, b, doc.ID, false)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	got = backupOf(t, repo, a, doc.ID)
	require.Equal(t, "mine", got.Title)
	require.Equal(t, docsbot.CatOther, got.Category)
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
