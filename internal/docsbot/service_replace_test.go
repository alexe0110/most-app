package docsbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestService_ReplaceContent(t *testing.T) {
	newFile := docsbot.IncomingFile{FileID: "tg-new", FileUniqueID: "uniq-new", FileSize: 99, MIME: "application/pdf"}

	tests := []struct {
		name       string
		mod        func(d *docsbot.Document)
		seedOther  bool // another saved document already has the new file
		in         docsbot.IncomingFile
		wantReply  func(t *testing.T, rep docsbot.Reply, doc docsbot.Document)
		wantErr    error
		wantFile   string // TgFileID of the document afterwards
		wantJobs   int
		wantDirty  bool
		wantSync   docsbot.SyncStatus
		wantMetaUp bool
	}{
		{
			name: "backed up: swapped and sync queued", in: newFile,
			wantReply: wantReplaced, wantFile: "tg-new", wantJobs: 1, wantDirty: true,
			wantSync: docsbot.SyncPending, wantMetaUp: true,
		},
		{
			name: "not on drive: database only", in: newFile,
			mod:       func(d *docsbot.Document) { d.BackupStatus, d.DriveFileID = docsbot.BackupNone, "" },
			wantReply: wantReplaced, wantFile: "tg-new", wantSync: docsbot.SyncOK, wantMetaUp: true,
		},
		{
			name: "failed upload: database only", in: newFile,
			mod:       func(d *docsbot.Document) { d.BackupStatus, d.DriveFileID = docsbot.BackupFailed, "" },
			wantReply: wantReplaced, wantFile: "tg-new", wantSync: docsbot.SyncOK, wantMetaUp: true,
		},
		{
			name: "too large: database only", in: newFile,
			mod:       func(d *docsbot.Document) { d.BackupStatus, d.DriveFileID = docsbot.BackupTooLarge, "" },
			wantReply: wantReplaced, wantFile: "tg-new", wantSync: docsbot.SyncOK, wantMetaUp: true,
		},
		{
			name: "same file as current: unchanged", in: docsbot.IncomingFile{FileID: "other-id", FileUniqueID: "uniq-cur"},
			wantReply: wantReplaced, wantFile: "tg-cur", wantSync: docsbot.SyncOK,
		},
		{
			name: "another document has the file", in: newFile, seedOther: true,
			wantReply: func(t *testing.T, rep docsbot.Reply, _ docsbot.Document) {
				got, ok := rep.(docsbot.AlreadySaved)
				require.True(t, ok, "%T", rep)
				require.Equal(t, "Другой", got.Doc.Title)
			},
			wantFile: "tg-cur", wantSync: docsbot.SyncOK,
		},
		{
			name: "uploading is busy", in: newFile,
			mod:       func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupUploading },
			wantReply: wantReply[docsbot.ReplaceBusy], wantFile: "tg-cur", wantSync: docsbot.SyncOK,
		},
		{
			name: "upload queued is busy", in: newFile,
			mod:       func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupPending },
			wantReply: wantReply[docsbot.ReplaceBusy], wantFile: "tg-cur", wantSync: docsbot.SyncOK,
		},
		{
			name: "sync in flight is busy", in: newFile,
			mod:       func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending },
			wantReply: wantReply[docsbot.ReplaceBusy], wantFile: "tg-cur", wantSync: docsbot.SyncPending,
		},
		{
			name: "drive file missing", in: newFile,
			mod:       func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing },
			wantReply: wantReply[docsbot.ReplaceMissing], wantFile: "tg-cur", wantSync: docsbot.SyncMissing,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			doc := backedUpDoc(repo, func(d *docsbot.Document) {
				d.TgFileID, d.TgFileUniqueID = "tg-cur", "uniq-cur"
				if tt.mod != nil {
					tt.mod(d)
				}
			})
			if tt.seedOther {
				repo.seed(docsbot.Document{
					UserID: user, Status: docsbot.StatusSaved, Title: "Другой", TgFileID: "o", TgFileUniqueID: tt.in.FileUniqueID,
				})
			}
			svc, syncs := syncService(repo, fakeDrive{}, fakeFiles{})

			rep, err := svc.ReplaceContent(t.Context(), user, doc.ID, tt.in)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
				tt.wantReply(t, rep, doc)
			}
			got := repo.docs[doc.ID]
			require.Equal(t, tt.wantFile, got.TgFileID)
			require.Equal(t, tt.wantSync, got.SyncStatus)
			require.Equal(t, tt.wantDirty, got.ContentDirty)
			require.Len(t, syncs.jobs, tt.wantJobs)
			require.Equal(t, doc.Title, got.Title, "title never changes")
			require.Equal(t, doc.Category, got.Category, "category never changes")
			if tt.wantMetaUp {
				require.Equal(t, doc.MetaVersion+1, got.MetaVersion)
			} else {
				require.Equal(t, doc.MetaVersion, got.MetaVersion)
			}
		})
	}
}

func wantReplaced(t *testing.T, rep docsbot.Reply, doc docsbot.Document) {
	t.Helper()
	got, ok := rep.(docsbot.Replaced)
	require.True(t, ok, "%T", rep)
	require.Equal(t, doc.ID, got.Doc.ID)
}

// wantReply checks the reply is of type T.
func wantReply[T docsbot.Reply](t *testing.T, rep docsbot.Reply, _ docsbot.Document) {
	t.Helper()
	require.IsType(t, *new(T), rep)
}

// racingSyncs makes ReplaceContent lose the unique-constraint race against a
// save of the same file that lands in between.
type racingSyncs struct {
	*fakeSyncs
	repo *fakeRepo
}

func (s racingSyncs) ReplaceContent(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile) (bool, error) {
	s.repo.seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Title: "Параллельный", TgFileID: "r", TgFileUniqueID: in.FileUniqueID,
	})
	return s.fakeSyncs.ReplaceContent(ctx, user, id, in)
}

func TestService_ReplaceContent_raceBecomesAlreadySaved(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.TgFileID, d.TgFileUniqueID = "tg-cur", "uniq-cur" })
	syncs := racingSyncs{fakeSyncs: &fakeSyncs{repo: repo}, repo: repo}
	svc := docsbot.NewService(repo, fakeDrive{}, fakeFiles{}, &fakeUploads{repo: repo}, repo, newFakeStatus(repo), docsbot.WithSyncs(syncs))

	rep, err := svc.ReplaceContent(t.Context(), user, doc.ID, docsbot.IncomingFile{FileID: "n", FileUniqueID: "uniq-new"})

	require.NoError(t, err)
	got, ok := rep.(docsbot.AlreadySaved)
	require.True(t, ok, "%T", rep)
	require.Equal(t, "Параллельный", got.Doc.Title)
	require.Equal(t, "tg-cur", repo.docs[doc.ID].TgFileID)
}

func TestService_ReplaceContent_unknownAndForeign(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	svc, _ := syncService(repo, fakeDrive{}, fakeFiles{})
	in := docsbot.IncomingFile{FileID: "n", FileUniqueID: "u"}

	_, err := svc.ReplaceContent(t.Context(), user, "nope", in)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.ReplaceContent(t.Context(), stranger, doc.ID, in)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestService_ReplaceContent_storageError(t *testing.T) {
	repo := newFakeRepo()
	doc := backedUpDoc(repo, nil)
	svc, syncs := syncService(repo, fakeDrive{}, fakeFiles{})
	syncs.err = errors.New("db down")

	_, err := svc.ReplaceContent(t.Context(), user, doc.ID, docsbot.IncomingFile{FileID: "n", FileUniqueID: "u"})

	require.Error(t, err)
}
