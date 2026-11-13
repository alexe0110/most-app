//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// makeUser is a fixture: it inserts a user and returns it, failing the test on
// any error. Each test gets a fresh database, so no cleanup is needed.
func makeUser(t *testing.T, repo *postgres.Repo, tgID int64) postgres.User {
	t.Helper()
	u, err := repo.EnsureUser(t.Context(), postgres.EnsureUserParams{TgUserID: tgID, Lang: "ru"})
	require.NoError(t, err)
	return u
}

func newRepo(t *testing.T) *postgres.Repo {
	t.Helper()
	return postgres.NewRepo(dbtest.New(t)) // boots Postgres + applies migrations
}

func TestRepo_users(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	u := makeUser(t, repo, 100)
	require.True(t, u.ID.Valid)
	require.EqualValues(t, 7, u.ID.Bytes[6]>>4, "id must be a UUID v7")

	got, err := repo.GetUserByTgID(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)

	_, err = repo.GetUserByTgID(ctx, 999)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	require.NoError(t, repo.SoftDeleteUser(ctx, u.ID))
	_, err = repo.GetUserByTgID(ctx, 100)
	require.ErrorIs(t, err, pgx.ErrNoRows, "soft-deleted user is hidden")
}

func TestRepo_statusCheck(t *testing.T) {
	repo := newRepo(t)
	u := makeUser(t, repo, 250)

	_, err := repo.InsertDocument(t.Context(), postgres.InsertDocumentParams{
		UserID: u.ID, Status: "bogus", Kind: "document", Category: "x",
		Title: "t", TgFileID: "f", TgFileUniqueID: "uq",
	})
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code, "check_violation on status")
}

func TestRepo_integrationsAndFolders(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 300)

	ig, err := repo.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
		UserID: u.ID, Bot: "docs", Provider: "google",
		RefreshTokenEnc: []byte{0x01, 0x02}, Status: "active",
	})
	require.NoError(t, err)

	// Upsert on the same key updates in place, not inserts.
	ig2, err := repo.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
		UserID: u.ID, Bot: "docs", Provider: "google",
		RefreshTokenEnc: []byte{0x03}, Status: "revoked",
	})
	require.NoError(t, err)
	require.Equal(t, ig.ID, ig2.ID)
	require.Equal(t, "revoked", ig2.Status)

	fold, err := repo.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
		UserID: u.ID, Category: "passport", DriveFolderID: "folder-1",
	})
	require.NoError(t, err)
	require.Equal(t, "folder-1", fold.DriveFolderID)

	fold2, err := repo.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
		UserID: u.ID, Category: "passport", DriveFolderID: "folder-2",
	})
	require.NoError(t, err)
	require.Equal(t, "folder-2", fold2.DriveFolderID)

	require.NoError(t, repo.DeleteDriveFolder(ctx, postgres.DeleteDriveFolderParams{UserID: u.ID, Category: "passport"}))
	_, err = repo.GetDriveFolder(ctx, postgres.GetDriveFolderParams{UserID: u.ID, Category: "passport"})
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestRepo_Tx(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 400)

	// Commit: the write lands.
	err := repo.Tx(ctx, func(q *postgres.Queries) error {
		_, err := q.InsertDocument(ctx, postgres.InsertDocumentParams{
			UserID: u.ID, Status: "pending", Kind: "document", Category: "",
			Title: "ok", TgFileID: "f1", TgFileUniqueID: "tx-1",
		})
		return err
	})
	require.NoError(t, err)

	// Rollback: returning an error discards the write.
	wantErr := errors.New("boom")
	err = repo.Tx(ctx, func(q *postgres.Queries) error {
		_, err := q.InsertDocument(ctx, postgres.InsertDocumentParams{
			UserID: u.ID, Status: "pending", Kind: "document", Category: "",
			Title: "discarded", TgFileID: "f2", TgFileUniqueID: "tx-2",
		})
		require.NoError(t, err)
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	_, err = repo.GetDocumentByUniqueID(ctx, postgres.GetDocumentByUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-1"})
	require.NoError(t, err, "committed row survives")
	_, err = repo.GetDocumentByUniqueID(ctx, postgres.GetDocumentByUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-2"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "rolled-back row is gone")
}

// mustSave saves a pending document and requires that this call did the saving.
func mustSave(t *testing.T, ctx context.Context, repo docsbot.Repo, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) {
	t.Helper()
	saved, err := repo.SaveDocument(ctx, user, id, cat)
	require.NoError(t, err)
	require.True(t, saved)
}

func TestSaveDocument_parallelTapsSaveOnce(t *testing.T) {
	repo, user := domainRepo(t)
	ctx := t.Context()
	doc, err := repo.CreateDocument(ctx, docsbot.IncomingFile{
		UserID: user, Kind: docsbot.KindDocument, FileID: "f-par", FileUniqueID: "u-par",
	})
	require.NoError(t, err)

	const taps = 8
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		wins  int
		start = make(chan struct{})
	)
	for range taps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			saved, err := repo.SaveDocument(ctx, user, doc.ID, docsbot.CatOther)
			assert.NoError(t, err)
			if saved {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, 1, wins)

	// A second call on the saved document and one on a missing id save nothing.
	saved, err := repo.SaveDocument(ctx, user, doc.ID, docsbot.CatOther)
	require.NoError(t, err)
	require.False(t, saved)
}
