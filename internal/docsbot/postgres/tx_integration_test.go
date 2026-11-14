//go:build integration

package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

func TestInTx_commitsOnNilAndRollsBackOnError(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	q := New(pool)
	u, err := q.EnsureUser(ctx, EnsureUserParams{TgUserID: 400, Lang: "ru"})
	require.NoError(t, err)

	insert := func(q *Queries, unique string) error {
		_, err := q.InsertDocument(ctx, InsertDocumentParams{
			UserID: u.ID, Status: "pending", Kind: "document",
			Title: unique, TgFileID: "f-" + unique, TgFileUniqueID: unique,
		})
		return err
	}

	require.NoError(t, inTx(ctx, pool, func(q *Queries, _ pgx.Tx) error { return insert(q, "tx-1") }))

	wantErr := errors.New("boom")
	err = inTx(ctx, pool, func(q *Queries, _ pgx.Tx) error {
		require.NoError(t, insert(q, "tx-2"))
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)

	_, err = q.GetDocumentByUniqueID(ctx, GetDocumentByUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-1"})
	require.NoError(t, err, "committed row survives")
	_, err = q.GetDocumentByUniqueID(ctx, GetDocumentByUniqueIDParams{UserID: u.ID, TgFileUniqueID: "tx-2"})
	require.ErrorIs(t, err, pgx.ErrNoRows, "rolled-back row is gone")
}
