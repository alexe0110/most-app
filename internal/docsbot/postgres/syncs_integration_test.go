//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// syncEnv is a repo over a fresh database with two users.
type syncEnv struct {
	pool            *pgxpool.Pool
	repo            *postgres.Repo
	user, otherUser docsbot.UserID
}

func newSyncEnv(t *testing.T) syncEnv {
	t.Helper()
	pool := dbtest.New(t)
	repo := postgres.NewRepo(pool)
	a := makeUser(t, repo, 801)
	b := makeUser(t, repo, 802)
	return syncEnv{pool: pool, repo: repo, user: docsbot.UserID(a.ID.String()), otherUser: docsbot.UserID(b.ID.String())}
}

// seed inserts a saved document with the given states and returns its id.
func (e syncEnv) seed(t *testing.T, user docsbot.UserID, unique, backup, syncStatus string) docsbot.DocumentID {
	t.Helper()
	var id string
	drive := ""
	if backup == "backed_up" {
		drive = "remote-" + unique
	}
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`insert into documents (user_id, status, kind, category, title, tg_file_id, tg_file_unique_id, backup_status, drive_file_id, sync_status)
		 values ($1::uuid, 'saved', 'document', 'other', 'Старое', $2, $3, $4, nullif($5, ''), $6) returning id::text`,
		string(user), "file-"+unique, unique, backup, drive, syncStatus).Scan(&id))
	return docsbot.DocumentID(id)
}

type row struct {
	Title, Category, Backup, Sync, TgFile string
	DriveFile                             string
	Meta                                  int
	Dirty                                 bool
}

func (e syncEnv) row(t *testing.T, id docsbot.DocumentID) row {
	t.Helper()
	var r row
	var drive *string
	require.NoError(t, e.pool.QueryRow(t.Context(),
		`select title, category, backup_status, sync_status, tg_file_id, drive_file_id, meta_version, content_dirty
		 from documents where id = $1::uuid`, string(id)).
		Scan(&r.Title, &r.Category, &r.Backup, &r.Sync, &r.TgFile, &drive, &r.Meta, &r.Dirty))
	if drive != nil {
		r.DriveFile = *drive
	}
	return r
}

func (e syncEnv) exists(t *testing.T, id docsbot.DocumentID) bool {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(t.Context(), `select count(*) from documents where id = $1::uuid`, string(id)).Scan(&n))
	return n == 1
}

// marker is the "job insert": it writes a row through the same transaction so a
// test can tell whether it committed or rolled back with the document change.
type marker struct {
	pool  *pgxpool.Pool
	calls int
	fail  error
}

func (m *marker) insert(ctx context.Context, tx pgx.Tx) error {
	m.calls++
	if _, err := tx.Exec(ctx, `insert into users (tg_user_id) values (9000 + $1)`, m.calls); err != nil {
		return err
	}
	return m.fail
}

// deleteDoc runs DeleteForSync with trash wanted, the marker as the trash insert.
func (m *marker) deleteDoc(ctx context.Context, repo *postgres.Repo, user docsbot.UserID, id docsbot.DocumentID) error {
	_, err := repo.DeleteForSync(ctx, user, id, true, func(ctx context.Context, tx pgx.Tx, _ string) error { return m.insert(ctx, tx) })
	return err
}

func (m *marker) committed(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, m.pool.QueryRow(t.Context(), `select count(*) from users where tg_user_id >= 9000`).Scan(&n))
	return n
}

func TestSyncs_titleAndCategoryDecideSyncInSQL(t *testing.T) {
	tests := []struct {
		name       string
		backup     string
		sync       string
		wantQueued bool
		wantSync   string
	}{
		{"backed up ok", "backed_up", "ok", true, "pending"},
		{"backed up failed", "backed_up", "failed", true, "pending"},
		{"backed up already pending", "backed_up", "pending", true, "pending"},
		{"backed up missing", "backed_up", "missing", false, "missing"},
		{"uploading", "uploading", "ok", false, "ok"},
		{"pending upload", "pending", "ok", false, "ok"},
		{"not on drive", "none", "ok", false, "ok"},
		{"failed upload", "failed", "ok", false, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSyncEnv(t)
			ctx := t.Context()

			titleDoc := e.seed(t, e.user, "t", tt.backup, tt.sync)
			m := &marker{pool: e.pool}
			queued, err := e.repo.SetTitleForSync(ctx, e.user, titleDoc, "Новое", m.insert)
			require.NoError(t, err)
			require.Equal(t, tt.wantQueued, queued)
			r := e.row(t, titleDoc)
			require.Equal(t, "Новое", r.Title)
			require.Equal(t, tt.wantSync, r.Sync)
			require.Equal(t, 1, r.Meta)
			require.Equal(t, btoi(tt.wantQueued), m.committed(t), "job inserted exactly when queued")

			catDoc := e.seed(t, e.user, "c", tt.backup, tt.sync)
			m2 := &marker{pool: e.pool}
			queued, err = e.repo.SetCategoryForSync(ctx, e.user, catDoc, docsbot.CatPassport, m2.insert)
			require.NoError(t, err)
			require.Equal(t, tt.wantQueued, queued)
			r = e.row(t, catDoc)
			require.Equal(t, string(docsbot.CatPassport), r.Category)
			require.Equal(t, tt.wantSync, r.Sync)
			require.Equal(t, 1, r.Meta)
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestSyncs_failedInsertRollsBackTheChange(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	boom := errors.New("queue down")

	id := e.seed(t, e.user, "a", "backed_up", "ok")
	m := &marker{pool: e.pool, fail: boom}
	_, err := e.repo.SetTitleForSync(ctx, e.user, id, "Новое", m.insert)
	require.ErrorIs(t, err, boom)
	require.Equal(t, row{Title: "Старое", Category: "other", Backup: "backed_up", Sync: "ok", TgFile: "file-a", DriveFile: "remote-a"}, e.row(t, id))
	require.Zero(t, m.committed(t))

	_, err = e.repo.SetCategoryForSync(ctx, e.user, id, docsbot.CatPassport, m.insert)
	require.ErrorIs(t, err, boom)
	require.Equal(t, "other", e.row(t, id).Category)

	require.ErrorIs(t, m.deleteDoc(ctx, e.repo, e.user, id), boom)
	require.True(t, e.exists(t, id), "delete rolled back with the failed job insert")

	require.ErrorIs(t, e.repo.EnqueueSyncJob(ctx, e.user, id, m.insert), boom)
	require.Equal(t, "ok", e.row(t, id).Sync)

	failed := e.seed(t, e.user, "f", "backed_up", "failed")
	_, err = e.repo.RetrySyncJob(ctx, e.user, failed, m.insert)
	require.ErrorIs(t, err, boom)
	require.Equal(t, "failed", e.row(t, failed).Sync)

	in := docsbot.IncomingFile{FileID: "new", FileUniqueID: "new-u", FileSize: 5, MIME: "application/pdf"}
	replaced, err := e.repo.ReplaceContentForSync(ctx, e.user, id, in, true, m.insert)
	require.ErrorIs(t, err, boom)
	require.False(t, replaced)
	require.Equal(t, "file-a", e.row(t, id).TgFile)

	_, err = e.repo.MarkBackedUpForSync(ctx, e.user, e.seed(t, e.user, "u", "uploading", "ok"), "drive-x", -1, m.insert)
	require.ErrorIs(t, err, boom)
}

func TestSyncs_markBackedUpComparesVersion(t *testing.T) {
	tests := []struct {
		name         string
		bump         int // renames done while "uploading"
		wantQueued   bool
		wantSyncStat string
	}{
		{"unchanged", 0, false, "ok"},
		{"renamed during upload", 1, true, "pending"},
		{"renamed twice during upload", 2, true, "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSyncEnv(t)
			ctx := t.Context()
			id := e.seed(t, e.user, "a", "uploading", "ok")
			start := e.row(t, id).Meta
			for range tt.bump {
				_, err := e.repo.SetTitleForSync(ctx, e.user, id, "Имя", (&marker{pool: e.pool}).insert)
				require.NoError(t, err)
			}

			m := &marker{pool: e.pool}
			queued, err := e.repo.MarkBackedUpForSync(ctx, e.user, id, "drive-1", start, m.insert)

			require.NoError(t, err)
			require.Equal(t, tt.wantQueued, queued)
			r := e.row(t, id)
			require.Equal(t, "backed_up", r.Backup)
			require.Equal(t, "drive-1", r.DriveFile)
			require.Equal(t, tt.wantSyncStat, r.Sync)
			require.Equal(t, btoi(tt.wantQueued), m.calls)
		})
	}
}

func TestSyncs_markBackedUpGoneAndForeign(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	id := e.seed(t, e.user, "a", "uploading", "ok")
	m := &marker{pool: e.pool}

	_, err := e.repo.MarkBackedUpForSync(ctx, e.otherUser, id, "drive-1", 0, m.insert)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	require.Equal(t, "uploading", e.row(t, id).Backup)
	require.Empty(t, e.row(t, id).DriveFile)

	require.NoError(t, e.repo.DeleteDocument(ctx, e.user, id))
	_, err = e.repo.MarkBackedUpForSync(ctx, e.user, id, "drive-1", 0, m.insert)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = e.repo.MarkBackedUpForSync(ctx, e.user, id, "drive-1", 0, noInsert)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	require.Zero(t, m.calls)
}

func TestSyncs_scopedByUser(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	id := e.seed(t, e.user, "a", "backed_up", "failed")
	before := e.row(t, id)
	m := &marker{pool: e.pool}

	_, err := e.repo.SetTitleForSync(ctx, e.otherUser, id, "Чужое", m.insert)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = e.repo.SetCategoryForSync(ctx, e.otherUser, id, docsbot.CatPassport, m.insert)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	require.NoError(t, e.repo.EnqueueSyncJob(ctx, e.otherUser, id, m.insert))
	queued, err := e.repo.RetrySyncJob(ctx, e.otherUser, id, m.insert)
	require.NoError(t, err)
	require.False(t, queued)
	replaced, err := e.repo.ReplaceContentForSync(ctx, e.otherUser, id, docsbot.IncomingFile{FileID: "x", FileUniqueID: "xu"}, true, m.insert)
	require.NoError(t, err)
	require.False(t, replaced)
	require.ErrorIs(t, m.deleteDoc(ctx, e.repo, e.otherUser, id), docsbot.ErrNotFound)
	ok, err := e.repo.MarkSynced(ctx, e.otherUser, id, before.Meta)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, e.repo.SetSyncStatus(ctx, e.otherUser, id, docsbot.SyncOK, ""))
	claimed, err := e.repo.ClaimFailed(ctx, e.otherUser, id)
	require.NoError(t, err)
	require.False(t, claimed)

	require.Equal(t, before, e.row(t, id), "a foreign user changes nothing")
	require.Zero(t, m.calls, "and queues nothing")
}

func TestSyncs_enqueueSyncGuards(t *testing.T) {
	tests := []struct {
		name, backup, sync string
		wantCalls          int
		wantSync           string
	}{
		{"backed up ok", "backed_up", "ok", 1, "pending"},
		{"backed up failed", "backed_up", "failed", 1, "pending"},
		{"missing file", "backed_up", "missing", 0, "missing"},
		{"not backed up", "pending", "ok", 0, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSyncEnv(t)
			id := e.seed(t, e.user, "a", tt.backup, tt.sync)
			m := &marker{pool: e.pool}

			require.NoError(t, e.repo.EnqueueSyncJob(t.Context(), e.user, id, m.insert))

			require.Equal(t, tt.wantCalls, m.calls)
			require.Equal(t, tt.wantSync, e.row(t, id).Sync)
		})
	}
	t.Run("gone document", func(t *testing.T) {
		e := newSyncEnv(t)
		m := &marker{pool: e.pool}
		require.NoError(t, e.repo.EnqueueSyncJob(t.Context(), e.user, "00000000-0000-0000-0000-000000000001", m.insert))
		require.Zero(t, m.calls)
	})
}

func TestSyncs_retrySyncClaimsOnce(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "failed")
	m := &marker{pool: e.pool}

	queued, err := e.repo.RetrySyncJob(t.Context(), e.user, id, m.insert)
	require.NoError(t, err)
	require.True(t, queued)
	queued, err = e.repo.RetrySyncJob(t.Context(), e.user, id, m.insert)
	require.NoError(t, err)
	require.False(t, queued, "second tap")
	require.Equal(t, 1, m.calls)
	require.Equal(t, "pending", e.row(t, id).Sync)
}

func TestSyncs_deleteAndInsert(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	m := &marker{pool: e.pool}

	require.NoError(t, m.deleteDoc(t.Context(), e.repo, e.user, id))
	require.False(t, e.exists(t, id))
	require.Equal(t, 1, m.committed(t), "delete and job commit together")

	require.ErrorIs(t, m.deleteDoc(t.Context(), e.repo, e.user, id), docsbot.ErrNotFound)
	require.Equal(t, 1, m.calls, "second delete queues nothing")
}

func TestSyncs_replaceContentGuards(t *testing.T) {
	in := docsbot.IncomingFile{FileID: "new", FileUniqueID: "new-u", FileSize: 77, MIME: "application/pdf"}
	tests := []struct {
		name, backup, sync string
		wantReplaced       bool
		wantCalls          int
		wantDirty          bool
		wantSync           string
	}{
		{"backed up", "backed_up", "ok", true, 1, true, "pending"},
		{"backed up after failed sync", "backed_up", "failed", true, 1, true, "pending"},
		{"sync in flight", "backed_up", "pending", false, 0, false, "pending"},
		{"drive file missing", "backed_up", "missing", false, 0, false, "missing"},
		{"uploading", "uploading", "ok", false, 0, false, "ok"},
		{"upload queued", "pending", "ok", false, 0, false, "ok"},
		{"not on drive", "none", "ok", true, 0, false, "ok"},
		{"failed upload", "failed", "ok", true, 0, false, "ok"},
		{"too large", "too_large", "ok", true, 0, false, "ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSyncEnv(t)
			id := e.seed(t, e.user, "a", tt.backup, tt.sync)
			m := &marker{pool: e.pool}

			replaced, err := e.repo.ReplaceContentForSync(t.Context(), e.user, id, in, true, m.insert)

			require.NoError(t, err)
			require.Equal(t, tt.wantReplaced, replaced)
			require.Equal(t, tt.wantCalls, m.calls)
			r := e.row(t, id)
			require.Equal(t, tt.wantDirty, r.Dirty)
			require.Equal(t, tt.wantSync, r.Sync)
			require.Equal(t, "Старое", r.Title)
			require.Equal(t, "other", r.Category)
			if tt.wantReplaced {
				require.Equal(t, "new", r.TgFile)
				require.Equal(t, 1, r.Meta)
			} else {
				require.Equal(t, "file-a", r.TgFile)
				require.Zero(t, r.Meta)
			}
		})
	}
}

func TestSyncs_replaceContentDuplicateFile(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	e.seed(t, e.user, "taken", "none", "ok")
	m := &marker{pool: e.pool}

	replaced, err := e.repo.ReplaceContentForSync(t.Context(), e.user, id,
		docsbot.IncomingFile{FileID: "x", FileUniqueID: "taken"}, true, m.insert)

	require.ErrorIs(t, err, docsbot.ErrDuplicateFile)
	require.False(t, replaced)
	require.Zero(t, m.calls)
	require.Equal(t, row{Title: "Старое", Category: "other", Backup: "backed_up", Sync: "ok", TgFile: "file-a", DriveFile: "remote-a"}, e.row(t, id))

	// The same file id under another user is not a duplicate.
	e.seed(t, e.otherUser, "other-user-file", "none", "ok")
	replaced, err = e.repo.ReplaceContentForSync(t.Context(), e.user, id,
		docsbot.IncomingFile{FileID: "y", FileUniqueID: "other-user-file"}, true, m.insert)
	require.NoError(t, err)
	require.True(t, replaced)
}

func TestSyncs_markSyncedChecksVersionAndState(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	_, err := e.repo.SetTitleForSync(ctx, e.user, id, "Новое", (&marker{pool: e.pool}).insert)
	require.NoError(t, err)
	_, err = e.pool.Exec(ctx, `update documents set content_dirty = true where id = $1::uuid`, string(id))
	require.NoError(t, err)

	stale, err := e.repo.MarkSynced(ctx, e.user, id, 0)
	require.NoError(t, err)
	require.False(t, stale, "meta_version moved on")
	require.Equal(t, "pending", e.row(t, id).Sync)

	won, err := e.repo.MarkSynced(ctx, e.user, id, 1)
	require.NoError(t, err)
	require.True(t, won)
	r := e.row(t, id)
	require.Equal(t, "ok", r.Sync)
	require.False(t, r.Dirty)

	again, err := e.repo.MarkSynced(ctx, e.user, id, 1)
	require.NoError(t, err)
	require.False(t, again, "no longer pending")
}

func TestSyncs_claimFailedTakesBackedUpMissing(t *testing.T) {
	e := newSyncEnv(t)
	ctx := t.Context()

	missing := e.seed(t, e.user, "m", "backed_up", "missing")
	okDoc := e.seed(t, e.user, "o", "backed_up", "ok")
	failed := e.seed(t, e.user, "f", "failed", "ok")
	uploading := e.seed(t, e.user, "u", "uploading", "ok")

	got, err := e.repo.ClaimFailed(ctx, e.user, missing)
	require.NoError(t, err)
	require.True(t, got)
	r := e.row(t, missing)
	require.Equal(t, "pending", r.Backup)
	require.Equal(t, "ok", r.Sync)
	require.Empty(t, r.DriveFile, "stale file id dropped")

	again, err := e.repo.ClaimFailed(ctx, e.user, missing)
	require.NoError(t, err)
	require.False(t, again, "second tap")

	for _, id := range []docsbot.DocumentID{okDoc, uploading} {
		got, err = e.repo.ClaimFailed(ctx, e.user, id)
		require.NoError(t, err)
		require.False(t, got)
	}
	got, err = e.repo.ClaimFailed(ctx, e.user, failed)
	require.NoError(t, err)
	require.True(t, got)
	require.Equal(t, "remote-o", e.row(t, okDoc).DriveFile, "a healthy file keeps its id")
}

// A document whose file was replaced and then went missing is re-uploaded from
// the new Telegram file, so the stale content_dirty flag must not survive the
// claim (otherwise the next sync re-sends the content that was just uploaded).
func TestSyncs_claimFailedClearsContentDirty(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "m", "backed_up", "missing")
	_, err := e.pool.Exec(t.Context(), `update documents set content_dirty = true where id = $1::uuid`, string(id))
	require.NoError(t, err)

	got, err := e.repo.ClaimFailed(t.Context(), e.user, id)

	require.NoError(t, err)
	require.True(t, got)
	require.False(t, e.row(t, id).Dirty)
}

func TestSyncs_replaceContentChangesKind(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	_, err := e.pool.Exec(t.Context(), `update documents set kind = 'photo' where id = $1::uuid`, string(id))
	require.NoError(t, err)
	m := &marker{pool: e.pool}

	replaced, err := e.repo.ReplaceContentForSync(t.Context(), e.user, id,
		docsbot.IncomingFile{Kind: docsbot.KindDocument, FileID: "pdf", FileUniqueID: "pdf-u", MIME: "application/pdf"}, true, m.insert)

	require.NoError(t, err)
	require.True(t, replaced)
	var kind string
	require.NoError(t, e.pool.QueryRow(t.Context(), `select kind from documents where id = $1::uuid`, string(id)).Scan(&kind))
	require.Equal(t, "document", kind, "a photo replaced by a PDF must be sent as a document")
}

func TestSyncs_replaceContentWithoutCloudChangesOnlyTheRow(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	m := &marker{pool: e.pool}

	replaced, err := e.repo.ReplaceContentForSync(t.Context(), e.user, id,
		docsbot.IncomingFile{Kind: docsbot.KindDocument, FileID: "n", FileUniqueID: "n-u"}, false, m.insert)

	require.NoError(t, err)
	require.True(t, replaced)
	got := e.row(t, id)
	require.Equal(t, "n", got.TgFile)
	require.Equal(t, "ok", got.Sync)
	require.False(t, got.Dirty)
	require.Zero(t, m.calls)
}

func TestSyncs_deleteForSyncDecidesOnTheDeletedRow(t *testing.T) {
	tests := []struct {
		name       string
		backup     string
		sync       string
		trash      bool
		wantKept   bool
		wantInsert int
	}{
		{"backed up, connected: trash queued", "backed_up", "ok", true, false, 1},
		{"backed up, disconnected: file kept", "backed_up", "ok", false, true, 0},
		{"remote file missing: nothing to trash or keep", "backed_up", "missing", true, false, 0},
		{"not on drive: plain delete", "none", "ok", true, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newSyncEnv(t)
			id := e.seed(t, e.user, "a", tt.backup, tt.sync)
			var remote string
			insert := func(_ context.Context, _ pgx.Tx, remoteID string) error { remote = remoteID; return nil }

			kept, err := e.repo.DeleteForSync(t.Context(), e.user, id, tt.trash, insert)

			require.NoError(t, err)
			require.Equal(t, tt.wantKept, kept)
			require.False(t, e.exists(t, id))
			if tt.wantInsert == 1 {
				require.Equal(t, "remote-a", remote)
			} else {
				require.Empty(t, remote)
			}
		})
	}
}

func TestSyncs_enqueueSyncBumpsVersionAndClock(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "ok")
	_, err := e.pool.Exec(t.Context(), `update documents set updated_at = now() - interval '2 days' where id = $1::uuid`, string(id))
	require.NoError(t, err)
	before := e.row(t, id)

	require.NoError(t, e.repo.EnqueueSyncJob(t.Context(), e.user, id, (&marker{pool: e.pool}).insert))

	after := e.row(t, id)
	require.Equal(t, before.Meta+1, after.Meta, "a stale parallel MarkSynced must lose")
	var fresh bool
	require.NoError(t, e.pool.QueryRow(t.Context(), `select updated_at > now() - interval '1 minute' from documents where id = $1::uuid`, string(id)).Scan(&fresh))
	require.True(t, fresh, "the stuck clock starts at enqueue")
}

func TestSyncs_stuckSyncsListsOnlyOldPendingBackedUp(t *testing.T) {
	e := newSyncEnv(t)
	_, err := e.pool.Exec(t.Context(), `insert into integrations (user_id, bot, provider, status) values ($1::uuid, 'docs', 'google', 'active')`, string(e.user))
	require.NoError(t, err)
	stuck := e.seed(t, e.user, "stuck", "backed_up", "pending")
	fresh := e.seed(t, e.user, "fresh", "backed_up", "pending")
	okDoc := e.seed(t, e.user, "ok", "backed_up", "ok")
	notBacked := e.seed(t, e.user, "nb", "uploading", "pending")
	for _, id := range []docsbot.DocumentID{stuck, okDoc, notBacked} {
		_, err := e.pool.Exec(t.Context(), `update documents set updated_at = now() - interval '2 hours' where id = $1::uuid`, string(id))
		require.NoError(t, err)
	}
	_ = fresh

	got, err := e.repo.StuckSyncs(t.Context(), time.Now().Add(-time.Hour), 10)

	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, stuck, got[0].ID)
}

func TestSyncs_claimFailedRetryKeepsClockFresh(t *testing.T) {
	e := newSyncEnv(t)
	id := e.seed(t, e.user, "a", "backed_up", "failed")
	_, err := e.pool.Exec(t.Context(), `update documents set updated_at = now() - interval '2 days' where id = $1::uuid`, string(id))
	require.NoError(t, err)

	queued, err := e.repo.RetrySyncJob(t.Context(), e.user, id, (&marker{pool: e.pool}).insert)

	require.NoError(t, err)
	require.True(t, queued)
	var fresh bool
	require.NoError(t, e.pool.QueryRow(t.Context(), `select updated_at > now() - interval '1 minute' from documents where id = $1::uuid`, string(id)).Scan(&fresh))
	require.True(t, fresh)
}
