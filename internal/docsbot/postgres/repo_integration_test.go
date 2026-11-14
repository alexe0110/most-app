//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// makeUser is a fixture: it inserts a user and returns it, failing the test on
// any error. Each test gets a fresh database, so no cleanup is needed.
func makeUser(t *testing.T, repo *adapters, tgID int64) postgres.User {
	t.Helper()
	u, err := repo.q.EnsureUser(t.Context(), postgres.EnsureUserParams{TgUserID: tgID, Lang: "ru"})
	require.NoError(t, err)
	return u
}

// adapters bundles every postgres adapter over one database, so a test reaches
// any port through one value; q is the sqlc layer for fixtures.
type adapters struct {
	*postgres.Documents
	*postgres.StatusMessages
	*postgres.Users
	*postgres.OAuthStore
	*postgres.FolderCache
	q *postgres.Queries
}

func newAdapters(pool *pgxpool.Pool, jobs postgres.JobInserter) *adapters {
	return &adapters{
		Documents:      postgres.NewDocuments(pool, jobs),
		StatusMessages: postgres.NewStatusMessages(pool),
		Users:          postgres.NewUsers(pool),
		OAuthStore:     postgres.NewOAuthStore(pool),
		FolderCache:    postgres.NewFolderCache(pool),
		q:              postgres.New(pool),
	}
}

func newRepo(t *testing.T) *adapters {
	t.Helper()
	return newAdapters(dbtest.New(t), noJobs{}) // boots Postgres + applies migrations
}

func TestRepo_users(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	u := makeUser(t, repo, 100)
	require.True(t, u.ID.Valid)
	require.EqualValues(t, 7, u.ID.Bytes[6]>>4, "id must be a UUID v7")

	got, err := repo.q.GetUserByTgID(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)

	_, err = repo.q.GetUserByTgID(ctx, 999)
	require.ErrorIs(t, err, pgx.ErrNoRows)

	require.NoError(t, repo.q.SoftDeleteUser(ctx, u.ID))
	_, err = repo.q.GetUserByTgID(ctx, 100)
	require.ErrorIs(t, err, pgx.ErrNoRows, "soft-deleted user is hidden")
}

func TestRepo_statusCheck(t *testing.T) {
	repo := newRepo(t)
	u := makeUser(t, repo, 250)

	_, err := repo.q.InsertDocument(t.Context(), postgres.InsertDocumentParams{
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

	ig, err := repo.q.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
		UserID: u.ID, Bot: "docs", Provider: "google",
		RefreshTokenEnc: []byte{0x01, 0x02}, Status: "active",
	})
	require.NoError(t, err)

	// Upsert on the same key updates in place, not inserts.
	ig2, err := repo.q.UpsertIntegration(ctx, postgres.UpsertIntegrationParams{
		UserID: u.ID, Bot: "docs", Provider: "google",
		RefreshTokenEnc: []byte{0x03}, Status: "revoked",
	})
	require.NoError(t, err)
	require.Equal(t, ig.ID, ig2.ID)
	require.Equal(t, "revoked", ig2.Status)

	fold, err := repo.q.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
		UserID: u.ID, Category: "passport", DriveFolderID: "folder-1",
	})
	require.NoError(t, err)
	require.Equal(t, "folder-1", fold.DriveFolderID)

	fold2, err := repo.q.UpsertDriveFolder(ctx, postgres.UpsertDriveFolderParams{
		UserID: u.ID, Category: "passport", DriveFolderID: "folder-2",
	})
	require.NoError(t, err)
	require.Equal(t, "folder-2", fold2.DriveFolderID)

	require.NoError(t, repo.q.DeleteDriveFolder(ctx, postgres.DeleteDriveFolderParams{UserID: u.ID, Category: "passport"}))
	_, err = repo.q.GetDriveFolder(ctx, postgres.GetDriveFolderParams{UserID: u.ID, Category: "passport"})
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

// mustSave saves a pending document and requires that this call did the saving.
func mustSave(t *testing.T, ctx context.Context, repo *adapters, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) {
	t.Helper()
	saved, err := repo.SaveChosen(ctx, docsbot.SaveChoice{User: user, ID: id, Category: cat})
	require.NoError(t, err)
	require.True(t, saved)
}

func TestSaveChosen_parallelTapsSaveOnce(t *testing.T) {
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
			saved, err := repo.SaveChosen(ctx, docsbot.SaveChoice{User: user, ID: doc.ID, Category: docsbot.CatOther})
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
	saved, err := repo.SaveChosen(ctx, docsbot.SaveChoice{User: user, ID: doc.ID, Category: docsbot.CatOther})
	require.NoError(t, err)
	require.False(t, saved)
}
