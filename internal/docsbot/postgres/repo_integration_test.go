//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// makeUser is a fixture: it inserts a user and returns it, failing the test on
// any error. Each test gets a fresh database, so no cleanup is needed.
func makeUser(t *testing.T, repo *postgres.Repo, tgID int64) postgres.User {
	t.Helper()
	u, err := repo.CreateUser(t.Context(), postgres.CreateUserParams{TgUserID: tgID, Lang: "ru"})
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

func TestRepo_documents(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()
	u := makeUser(t, repo, 200)

	doc, err := repo.CreateDocument(ctx, postgres.CreateDocumentParams{
		UserID:         u.ID,
		Status:         "pending",
		Category:       "passport",
		Title:          "Паспорт РК",
		TgFileID:       "file-id-1",
		TgFileUniqueID: "uniq-1",
	})
	require.NoError(t, err)
	require.False(t, doc.DriveFileID.Valid, "drive_file_id is null until saved")

	// Same (user_id, tg_file_unique_id) must be rejected.
	_, err = repo.CreateDocument(ctx, postgres.CreateDocumentParams{
		UserID: u.ID, Status: "pending", Category: "passport",
		Title: "дубль", TgFileID: "file-id-2", TgFileUniqueID: "uniq-1",
	})
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23505", pgErr.Code, "unique_violation")

	got, err := repo.GetDocumentByFileUniqueID(ctx, postgres.GetDocumentByFileUniqueIDParams{
		UserID: u.ID, TgFileUniqueID: "uniq-1",
	})
	require.NoError(t, err)
	require.Equal(t, doc.ID, got.ID)

	require.NoError(t, repo.SetDocumentStatus(ctx, postgres.SetDocumentStatusParams{ID: doc.ID, Status: "saved"}))

	byCat, err := repo.ListDocumentsByCategory(ctx, postgres.ListDocumentsByCategoryParams{UserID: u.ID, Category: "passport"})
	require.NoError(t, err)
	require.Len(t, byCat, 1)
	require.Equal(t, "saved", byCat[0].Status)

	// pg_trgm-backed search (gin index) finds by a fragment of the title.
	found, err := repo.SearchDocumentsByTitle(ctx, postgres.SearchDocumentsByTitleParams{
		UserID: u.ID, Column2: pgtype.Text{String: "паспорт", Valid: true}, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, doc.ID, found[0].ID)
}

func TestRepo_statusCheck(t *testing.T) {
	repo := newRepo(t)
	u := makeUser(t, repo, 250)

	_, err := repo.CreateDocument(t.Context(), postgres.CreateDocumentParams{
		UserID: u.ID, Status: "bogus", Category: "x",
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

	// Commit: both writes land.
	err := repo.Tx(ctx, func(q *postgres.Queries) error {
		_, err := q.CreateDocument(ctx, postgres.CreateDocumentParams{
			UserID: u.ID, Status: "pending", Category: "c",
			Title: "ok", TgFileID: "f1", TgFileUniqueID: "tx-1",
		})
		return err
	})
	require.NoError(t, err)

	// Rollback: returning an error discards the write.
	wantErr := errors.New("boom")
	err = repo.Tx(ctx, func(q *postgres.Queries) error {
		_, err := q.CreateDocument(ctx, postgres.CreateDocumentParams{
			UserID: u.ID, Status: "pending", Category: "c",
			Title: "discarded", TgFileID: "f2", TgFileUniqueID: "tx-2",
		})
		require.NoError(t, err)
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	_, err = repo.GetDocumentByFileUniqueID(ctx, postgres.GetDocumentByFileUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-1"})
	require.NoError(t, err, "committed row survives")
	_, err = repo.GetDocumentByFileUniqueID(ctx, postgres.GetDocumentByFileUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-2"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "rolled-back row is gone")
}
