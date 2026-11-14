//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/jobs"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
	pjobs "github.com/alexe0110/most-app/internal/platform/jobs"
)

// txEnv is the real postgres adapters over the real River queue, which is never
// started: every inserted job stays in river_job for the test to count.
type txEnv struct {
	pool *pgxpool.Pool
	jq   *jobs.Queue
	user docsbot.UserID
}

var txUserSeq atomic.Int64

func newTxEnv(t *testing.T) txEnv {
	t.Helper()
	pool := dbtest.New(t)
	log := slog.New(slog.DiscardHandler)
	inserter, err := pjobs.NewInserter(pool, log)
	require.NoError(t, err)
	return txEnv{pool: pool, jq: jobs.NewQueue(inserter)}.fresh(t)
}

// fresh returns the env on the same database with a new user, so subtests share
// one container yet never see each other's documents or jobs.
func (e txEnv) fresh(t *testing.T) txEnv {
	t.Helper()
	var uid string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`insert into users (tg_user_id) values ($1) returning id::text`, 50_000+txUserSeq.Add(1)).Scan(&uid))
	e.user = docsbot.UserID(uid)
	return e
}

func (e txEnv) docs(ji postgres.JobInserter) *postgres.Documents {
	return postgres.NewDocuments(e.pool, ji)
}

// service is a Service whose Documents insert jobs through ji; Drive is connected or not.
func (e txEnv) service(ji postgres.JobInserter, connected bool) *docsbot.Service {
	return newService(e.pool, e.docs(ji), e.jq, driveStatus{connected: connected}, fakeFiles{})
}

// seed inserts a document with the given states and returns its id.
func (e txEnv) seed(t *testing.T, status, backup, syncStatus string) docsbot.DocumentID {
	t.Helper()
	const unique = "a"
	category, drive := "other", ""
	if status == "pending" {
		category = ""
	}
	if backup == "backed_up" {
		drive = "remote-" + unique
	}
	var id string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status, drive_file_id, sync_status, mime_type)
		 values ($1::uuid, $2, 'document', $3, 'Старое', $4, $5, $6, nullif($7, ''), $8, 'application/pdf') returning id::text`,
		string(e.user), status, category, "file-"+unique, unique, backup, drive, syncStatus).Scan(&id))
	return docsbot.DocumentID(id)
}

// snapshot is the whole document row, so "unchanged" covers every column.
func (e txEnv) snapshot(t *testing.T, id docsbot.DocumentID) string {
	t.Helper()
	var s string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`select coalesce((select to_jsonb(d)::text from documents d where id = $1::uuid), 'gone')`, string(id)).Scan(&s))
	return s
}

// state is the columns a category choice moves.
type state struct{ Status, Category, Backup string }

func (e txEnv) state(t *testing.T, id docsbot.DocumentID) (s state, title string) {
	t.Helper()
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`select status, category, backup_status, title from documents where id = $1::uuid`, string(id)).Scan(&s.Status, &s.Category, &s.Backup, &title))
	return s, title
}

// jobCount counts the user's queued jobs of a kind; "" counts every kind.
func (e txEnv) jobCount(t *testing.T, kind string) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`select count(*) from river_job where args->>'UserID' = $1 and ($2 = '' or kind = $2)`, string(e.user), kind).Scan(&n))
	return n
}

// receive stores a pending document through the real Receive.
func (e txEnv) receive(t *testing.T, svc *docsbot.Service, unique string, size int64) docsbot.Document {
	t.Helper()
	rep, err := svc.Receive(t.Context(), docsbot.IncomingFile{
		UserID: e.user, Kind: docsbot.KindDocument, FileID: "f-" + unique, FileUniqueID: unique, FileSize: size,
	})
	require.NoError(t, err)
	need, ok := rep.(docsbot.NeedCategory)
	require.True(t, ok)
	return need.Doc
}

// driveStatus is a Drive that is, or is not, connected.
type driveStatus struct {
	fakeDrive
	connected bool
}

func (d driveStatus) Connected(context.Context, docsbot.UserID) (bool, error) {
	return d.connected, nil
}

// failAfterInsert inserts the job for real, then fails, so a test sees whether a
// job that already reached river_job rolls back with the failed transaction.
type failAfterInsert struct {
	postgres.JobInserter
	err error
}

func (f failAfterInsert) InsertUpload(ctx context.Context, tx pgx.Tx, u docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	if _, err := f.JobInserter.InsertUpload(ctx, tx, u, id); err != nil {
		return false, err
	}
	return false, f.err
}

func (f failAfterInsert) InsertSync(ctx context.Context, tx pgx.Tx, u docsbot.UserID, id docsbot.DocumentID) error {
	if err := f.JobInserter.InsertSync(ctx, tx, u, id); err != nil {
		return err
	}
	return f.err
}

func (f failAfterInsert) InsertTrash(ctx context.Context, tx pgx.Tx, u docsbot.UserID, remoteID string) error {
	if err := f.JobInserter.InsertTrash(ctx, tx, u, remoteID); err != nil {
		return err
	}
	return f.err
}

func TestQueue_failedTxLeavesNoJobAndNoRowChange(t *testing.T) {
	in := docsbot.IncomingFile{Kind: docsbot.KindDocument, FileID: "new", FileUniqueID: "new-u", FileSize: 5, MIME: "application/pdf"}
	var (
		upKind    = jobs.UploadDocumentArgs{}.Kind()
		syncKind  = jobs.StorageSyncArgs{}.Kind()
		trashKind = jobs.StorageTrashArgs{}.Kind()
	)
	type op func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error
	tests := []struct {
		name                      string
		status, backup, syncState string
		wantKind                  string
		run                       op
	}{
		{"set title", "saved", "backed_up", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.SetTitle(ctx, u, id, "Новое", true)
			return err
		}},
		{"set category", "saved", "backed_up", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.SetCategory(ctx, u, id, docsbot.CatPassport, true)
			return err
		}},
		{"mark backed up after a rename", "saved", "uploading", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.MarkBackedUp(ctx, u, id, "drive-x", -1, 0)
			return err
		}},
		{"replace content", "saved", "backed_up", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.ReplaceContent(ctx, u, id, in, true)
			return err
		}},
		{"enqueue sync", "saved", "backed_up", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			return d.EnqueueSync(ctx, u, id)
		}},
		{"retry sync", "saved", "backed_up", "failed", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.RetrySync(ctx, u, id)
			return err
		}},
		{"delete with trash", "saved", "backed_up", "ok", trashKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.DeleteDocument(ctx, u, id, true)
			return err
		}},
		{"requeue upload", "saved", "none", "ok", upKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			return d.RequeueUpload(ctx, u, id)
		}},
		{"requeue sync", "saved", "backed_up", "ok", syncKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			return d.RequeueSync(ctx, u, id)
		}},
		{"save chosen with upload", "pending", "none", "ok", upKind, func(ctx context.Context, d *postgres.Documents, u docsbot.UserID, id docsbot.DocumentID) error {
			_, err := d.SaveChosen(ctx, docsbot.SaveChoice{User: u, ID: id, Category: docsbot.CatPassport, Title: "Имя", Backup: docsbot.BackupPending})
			return err
		}},
	}
	shared := newTxEnv(t)
	boom := errors.New("queue down")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := shared.fresh(t)
			ctx := t.Context()
			id := e.seed(t, tt.status, tt.backup, tt.syncState)
			before := e.snapshot(t, id)

			err := tt.run(ctx, e.docs(failAfterInsert{JobInserter: e.jq, err: boom}), e.user, id)

			require.ErrorIs(t, err, boom)
			require.Zero(t, e.jobCount(t, ""), "the job inserted before the failure rolled back")
			require.Equal(t, before, e.snapshot(t, id), "the document is untouched")

			// Control: the same call on the healthy queue commits one job, so the zero
			// above is the rollback and not a call that never inserts.
			require.NoError(t, tt.run(ctx, e.docs(e.jq), e.user, id))
			require.Equal(t, 1, e.jobCount(t, tt.wantKind))
			require.Equal(t, 1, e.jobCount(t, ""))
		})
	}
}

func TestRequeue_dedupsUploadsAndLeavesTheDocumentAlone(t *testing.T) {
	e := newTxEnv(t)
	ctx := t.Context()
	id := e.seed(t, "saved", "backed_up", "ok")
	_, err := e.pool.Exec(ctx, `update documents set updated_at = now() - interval '2 days' where id = $1::uuid`, string(id))
	require.NoError(t, err)
	before := e.snapshot(t, id)
	docs := e.docs(e.jq)
	upload := jobs.UploadDocumentArgs{}.Kind()

	require.NoError(t, docs.RequeueUpload(ctx, e.user, id))
	require.NoError(t, docs.RequeueUpload(ctx, e.user, id))
	require.Equal(t, 1, e.jobCount(t, upload), "a live upload job dedups")

	// A finished job no longer blocks: a retry or a post-reconnect upload must run.
	_, err = e.pool.Exec(ctx, `update river_job set state = 'completed', finalized_at = now() where args->>'UserID' = $1`, string(e.user))
	require.NoError(t, err)
	require.NoError(t, docs.RequeueUpload(ctx, e.user, id))
	require.Equal(t, 2, e.jobCount(t, upload))

	// Sync jobs are not unique on purpose: a change during a running sync needs its follow-up.
	require.NoError(t, docs.RequeueSync(ctx, e.user, id))
	require.NoError(t, docs.RequeueSync(ctx, e.user, id))
	require.Equal(t, 2, e.jobCount(t, jobs.StorageSyncArgs{}.Kind()))

	require.Equal(t, before, e.snapshot(t, id), "requeue keeps the stuck clock and every status")
}
