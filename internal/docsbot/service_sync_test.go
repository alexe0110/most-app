package docsbot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

// syncService builds a Service with the sync port over repo.
func syncService(repo *docsbottest.FakeRepo, drive docsbottest.FakeDrive, files docsbottest.FakeFiles) (*docsbot.Service, *docsbottest.FakeSyncs) {
	syncs := &docsbottest.FakeSyncs{Repo: repo}
	svc := docsbottest.NewService(repo, drive, files, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), syncs)
	return svc, syncs
}

func backedUpDoc(repo *docsbottest.FakeRepo, mod func(*docsbot.Document)) docsbot.Document {
	d := docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "Договор",
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "remote-1", SyncStatus: docsbot.SyncOK,
		MIME: "application/pdf", Kind: docsbot.KindDocument,
	}
	if mod != nil {
		mod(&d)
	}
	return repo.Seed(d)
}

func TestService_Rename_sync(t *testing.T) {
	tests := []struct {
		name       string
		connected  bool
		mod        func(*docsbot.Document)
		wantSync   docsbot.SyncStatus
		wantJobs   int
		wantNewVer int
	}{
		{"backed up and connected queues a sync", true, nil, docsbot.SyncPending, 1, 1},
		{"not connected only renames", false, nil, docsbot.SyncOK, 0, 1},
		{"not backed up only renames", true, func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupPending; d.DriveFileID = "" }, docsbot.SyncOK, 0, 1},
		{"missing remote file is left alone", true, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing }, docsbot.SyncMissing, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			doc := backedUpDoc(repo, tt.mod)
			svc, syncs := syncService(repo, docsbottest.FakeDrive{IsConnected: tt.connected}, docsbottest.FakeFiles{})

			card, err := svc.Rename(t.Context(), user, doc.ID, "  Новое   имя ")

			require.NoError(t, err)
			require.Equal(t, "Новое имя", card.Doc.Title)
			require.Equal(t, tt.wantSync, card.Doc.SyncStatus)
			require.Equal(t, tt.wantNewVer, card.Doc.MetaVersion)
			require.Len(t, syncs.Jobs, tt.wantJobs)
		})
	}
}

func TestService_Rename_errors(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := backedUpDoc(repo, nil)
	svc, _ := syncService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{})

	_, err := svc.Rename(t.Context(), user, doc.ID, "   ")
	require.ErrorIs(t, err, docsbot.ErrEmptyTitle)
	_, err = svc.Rename(t.Context(), stranger, doc.ID, "x")
	require.ErrorIs(t, err, docsbot.ErrNotFound)
}

func TestService_Recategorize_sync(t *testing.T) {
	tests := []struct {
		name         string
		connected    bool
		wantSync     docsbot.SyncStatus
		wantJobs     int
		wantOldPlace bool
	}{
		{"connected queues a sync", true, docsbot.SyncPending, 1, false},
		{"not connected leaves the file in the old folder", false, docsbot.SyncOK, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			doc := backedUpDoc(repo, nil)
			svc, syncs := syncService(repo, docsbottest.FakeDrive{IsConnected: tt.connected}, docsbottest.FakeFiles{})

			card, err := svc.Recategorize(t.Context(), user, doc.ID, docsbot.CatPassport)

			require.NoError(t, err)
			require.Equal(t, docsbot.CatPassport, card.Doc.Category)
			require.Equal(t, tt.wantSync, card.Doc.SyncStatus)
			require.Equal(t, tt.wantOldPlace, card.FileInOldFolder)
			require.Len(t, syncs.Jobs, tt.wantJobs)
		})
	}

	svc, _ := syncService(docsbottest.NewFakeRepo(), docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{})
	_, err := svc.Recategorize(t.Context(), user, "nope", docsbot.CatPassport)
	require.ErrorIs(t, err, docsbot.ErrNotFound)
	_, err = svc.Recategorize(t.Context(), user, "nope", "bogus")
	require.ErrorIs(t, err, docsbot.ErrUnknownCategory)
}

func TestService_Delete_sync(t *testing.T) {
	tests := []struct {
		name       string
		connected  bool
		mod        func(*docsbot.Document)
		want       docsbot.Deleted
		wantTrashs []string
	}{
		{"in drive and connected trashes the file", true, nil, docsbot.Deleted{}, []string{"remote-1"}},
		{"in drive but not connected keeps the file", false, nil, docsbot.Deleted{RemoteKept: true}, nil},
		{"missing file and disconnected: nothing kept", false, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing }, docsbot.Deleted{}, nil},
		{"not in drive is a plain delete", true, func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupNone; d.DriveFileID = "" }, docsbot.Deleted{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			doc := backedUpDoc(repo, tt.mod)
			svc, syncs := syncService(repo, docsbottest.FakeDrive{IsConnected: tt.connected}, docsbottest.FakeFiles{})

			got, err := svc.Delete(t.Context(), user, doc.ID)

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantTrashs, syncs.Trashes)
			require.Empty(t, repo.Docs, "the row is always deleted")
		})
	}
}

func TestService_SyncRemote(t *testing.T) {
	tests := []struct {
		name        string
		mod         func(*docsbot.Document)
		drive       func(calls *[]string) docsbottest.FakeDrive
		wantErr     error
		wantNoop    bool
		wantMissing bool
		wantSync    docsbot.SyncStatus
		wantReason  string
		wantCalls   []string
	}{
		{
			name:      "moves and renames then marks ok",
			mod:       func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending; d.Title = "a/b" },
			drive:     func(c *[]string) docsbottest.FakeDrive { return docsbottest.FakeDrive{Calls: c} },
			wantSync:  docsbot.SyncOK,
			wantCalls: []string{"move:remote-1:other", "rename:remote-1:a_b.pdf"},
		},
		{
			name:      "dirty content is replaced first",
			mod:       func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending; d.ContentDirty = true },
			drive:     func(c *[]string) docsbottest.FakeDrive { return docsbottest.FakeDrive{Calls: c} },
			wantSync:  docsbot.SyncOK,
			wantCalls: []string{"replace:remote-1", "move:remote-1:other", "rename:remote-1:Договор.pdf"},
		},
		{
			name:     "already in sync is a no-op",
			drive:    func(c *[]string) docsbottest.FakeDrive { return docsbottest.FakeDrive{Calls: c} },
			wantNoop: true, wantSync: docsbot.SyncOK,
		},
		{
			name:     "missing status is a no-op",
			mod:      func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing },
			drive:    func(c *[]string) docsbottest.FakeDrive { return docsbottest.FakeDrive{Calls: c} },
			wantNoop: true, wantSync: docsbot.SyncMissing,
		},
		{
			name:     "not backed up is a no-op",
			mod:      func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupPending; d.SyncStatus = docsbot.SyncPending },
			drive:    func(c *[]string) docsbottest.FakeDrive { return docsbottest.FakeDrive{Calls: c} },
			wantNoop: true, wantSync: docsbot.SyncPending,
		},
		{
			name: "remote file gone marks missing",
			mod:  func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending },
			drive: func(c *[]string) docsbottest.FakeDrive {
				return docsbottest.FakeDrive{Calls: c, MoveErr: docsbot.ErrRemoteMissing}
			},
			wantMissing: true, wantSync: docsbot.SyncMissing,
			wantCalls: []string{"move:remote-1:other"},
		},
		{
			name: "revoked is returned for the worker",
			mod:  func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending },
			drive: func(c *[]string) docsbottest.FakeDrive {
				return docsbottest.FakeDrive{Calls: c, MoveErr: docsbot.ErrDriveRevoked}
			},
			wantErr:  docsbot.ErrDriveRevoked,
			wantSync: docsbot.SyncPending, wantCalls: []string{"move:remote-1:other"},
		},
		{
			name: "disconnected marks failed without a reason",
			mod:  func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending },
			drive: func(c *[]string) docsbottest.FakeDrive {
				return docsbottest.FakeDrive{Calls: c, RenameErr: docsbot.ErrDriveDisconnected}
			},
			wantNoop: true, wantSync: docsbot.SyncFailed,
			wantCalls: []string{"move:remote-1:other", "rename:remote-1:Договор.pdf"},
		},
		{
			name: "transient error is returned for retry",
			mod:  func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending },
			drive: func(c *[]string) docsbottest.FakeDrive {
				return docsbottest.FakeDrive{Calls: c, RenameErr: errors.New("503")}
			},
			wantErr:   errors.New("503"),
			wantSync:  docsbot.SyncPending,
			wantCalls: []string{"move:remote-1:other", "rename:remote-1:Договор.pdf"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			doc := backedUpDoc(repo, tt.mod)
			var calls []string
			svc, _ := syncService(repo, tt.drive(&calls), docsbottest.FakeFiles{Data: []byte("x"), MIME: "application/pdf"})

			res, err := svc.SyncRemote(t.Context(), user, doc.ID)

			if tt.wantErr != nil {
				require.Error(t, err)
				if !errors.Is(tt.wantErr, docsbot.ErrDriveRevoked) {
					require.ErrorContains(t, err, tt.wantErr.Error())
				} else {
					require.ErrorIs(t, err, tt.wantErr)
				}
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantNoop, res.Noop)
			require.Equal(t, tt.wantMissing, res.Missing)
			require.Equal(t, tt.wantSync, repo.Docs[doc.ID].SyncStatus)
			require.Equal(t, tt.wantReason, repo.Docs[doc.ID].SyncReason)
			require.Equal(t, tt.wantCalls, calls)
			if tt.wantSync == docsbot.SyncOK {
				require.False(t, repo.Docs[doc.ID].ContentDirty)
			}
		})
	}
}

func TestService_SyncRemote_goneDocumentIsNoop(t *testing.T) {
	svc, _ := syncService(docsbottest.NewFakeRepo(), docsbottest.FakeDrive{}, docsbottest.FakeFiles{})
	res, err := svc.SyncRemote(t.Context(), user, "nope")
	require.NoError(t, err)
	require.True(t, res.Noop)
}

// movedDuringSync bumps the document's meta_version while the sync talks to
// Drive, like a rename arriving mid-flight.
type movedDuringSync struct {
	docsbottest.FakeDrive
	repo *docsbottest.FakeRepo
	id   docsbot.DocumentID
}

func (d movedDuringSync) Rename(ctx context.Context, user docsbot.UserID, remoteID, name string) error {
	_ = d.repo.SetTitle(ctx, user, d.id, "changed meanwhile")
	return d.FakeDrive.Rename(ctx, user, remoteID, name)
}

func TestService_SyncRemote_changeDuringSyncQueuesFollowUp(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending })
	syncs := &docsbottest.FakeSyncs{Repo: repo}
	drive := movedDuringSync{repo: repo, id: doc.ID}
	svc := docsbottest.NewService(repo, drive, docsbottest.FakeFiles{}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), syncs)

	res, err := svc.SyncRemote(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.False(t, res.Noop)
	require.Equal(t, []docsbot.DocumentID{doc.ID}, syncs.Jobs, "follow-up sync queued")
	require.Equal(t, docsbot.SyncPending, repo.Docs[doc.ID].SyncStatus)
}

func TestService_RetrySync(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	failed := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncFailed })
	ok := backedUpDoc(repo, nil)
	svc, syncs := syncService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{})

	require.NoError(t, svc.RetrySync(t.Context(), user, failed.ID))
	require.Equal(t, docsbot.SyncPending, repo.Docs[failed.ID].SyncStatus)
	require.Len(t, syncs.Jobs, 1)
	require.ErrorIs(t, svc.RetrySync(t.Context(), user, failed.ID), docsbot.ErrNotRetryable, "second tap")
	require.ErrorIs(t, svc.RetrySync(t.Context(), user, ok.ID), docsbot.ErrNotRetryable)
	require.ErrorIs(t, svc.RetrySync(t.Context(), stranger, failed.ID), docsbot.ErrNotRetryable)
}

func TestService_RetryBackup_missingRemoteReuploads(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing })
	svc, _ := syncService(repo, docsbottest.FakeDrive{IsConnected: true}, docsbottest.FakeFiles{})

	require.NoError(t, svc.RetryBackup(t.Context(), user, doc.ID))

	got := repo.Docs[doc.ID]
	require.Equal(t, docsbot.BackupPending, got.BackupStatus)
	require.Equal(t, docsbot.SyncOK, got.SyncStatus)
	require.Empty(t, got.DriveFileID)
}

func TestService_MarkSyncFailed(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := backedUpDoc(repo, func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncPending })
	svc, _ := syncService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{})

	require.NoError(t, svc.MarkSyncFailed(t.Context(), user, doc.ID, docsbot.SyncReasonRevoked))

	require.Equal(t, docsbot.SyncFailed, repo.Docs[doc.ID].SyncStatus)
	require.Equal(t, docsbot.SyncReasonRevoked, repo.Docs[doc.ID].SyncReason)
}

func TestService_TrashRemote(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{"ok", nil, nil},
		{"already gone counts as done", docsbot.ErrRemoteMissing, nil},
		{"revoked is returned", docsbot.ErrDriveRevoked, docsbot.ErrDriveRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := syncService(docsbottest.NewFakeRepo(), docsbottest.FakeDrive{TrashErr: tt.err}, docsbottest.FakeFiles{})
			err := svc.TrashRemote(t.Context(), user, "remote-1")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestService_Backup_changeDuringUploadQueuesSync(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "a.jpg", Kind: docsbot.KindPhoto,
		BackupStatus: docsbot.BackupPending, TgFileID: "tg-1", MIME: "application/pdf",
	})
	syncs := &docsbottest.FakeSyncs{Repo: repo}
	var name string
	drive := renamingDrive{
		FakeDrive: docsbottest.FakeDrive{IsConnected: true, UploadFile: docsbot.DriveFile{ID: "remote-9"}, UploadedAs: &name},
		onUpload:  func() { _ = repo.SetTitle(t.Context(), user, doc.ID, "renamed") },
	}
	svc := docsbottest.NewService(repo, drive, docsbottest.FakeFiles{Data: []byte("x"), MIME: "application/pdf"}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), syncs)

	res, err := svc.Backup(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.False(t, res.Noop)
	require.Equal(t, "a.pdf", name, "extension follows the MIME")
	require.Equal(t, []docsbot.DocumentID{doc.ID}, syncs.Jobs)
	require.Equal(t, docsbot.SyncPending, repo.Docs[doc.ID].SyncStatus)
}

// Scenario: a rename lands during the upload and the atomic "mark backed up +
// queue sync" fails. Nothing may be half-done: the document stays uploading, so
// the retry runs the whole step again instead of seeing backed_up and dropping the sync.
func TestService_Backup_markBackedUpFailureIsAtomic(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "a", Kind: docsbot.KindDocument,
		BackupStatus: docsbot.BackupPending, TgFileID: "tg-1", MIME: "application/pdf",
	})
	syncs := &docsbottest.FakeSyncs{Repo: repo}
	uploads := 0
	drive := renamingDrive{
		FakeDrive: docsbottest.FakeDrive{IsConnected: true, UploadFile: docsbot.DriveFile{ID: "remote-9"}},
		onUpload: func() {
			if uploads++; uploads > 1 {
				return
			}
			_ = repo.SetTitle(t.Context(), user, doc.ID, "renamed")
			syncs.Err = errors.New("db down") // the next syncs call fails
		},
	}
	svc := docsbottest.NewService(repo, drive, docsbottest.FakeFiles{Data: []byte("x"), MIME: "application/pdf"}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), syncs)

	_, err := svc.Backup(t.Context(), user, doc.ID)

	require.Error(t, err)
	require.Equal(t, docsbot.BackupUploading, repo.Docs[doc.ID].BackupStatus)
	require.Empty(t, syncs.Jobs)

	syncs.Err = nil
	res, err := svc.Backup(t.Context(), user, doc.ID)
	require.NoError(t, err)
	require.False(t, res.Noop, "the retry runs the step again")
	require.Equal(t, docsbot.BackupBackedUp, repo.Docs[doc.ID].BackupStatus)
}

func TestService_Backup_deletedDuringUploadTrashesFile(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	doc := repo.Seed(docsbot.Document{
		UserID: user, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "a",
		BackupStatus: docsbot.BackupPending, TgFileID: "tg-1",
	})
	var calls []string
	drive := renamingDrive{
		FakeDrive: docsbottest.FakeDrive{IsConnected: true, UploadFile: docsbot.DriveFile{ID: "remote-9"}, Calls: &calls},
		onUpload:  func() { _ = repo.DeleteDocument(t.Context(), user, doc.ID) },
	}
	svc := docsbottest.NewService(repo, drive, docsbottest.FakeFiles{Data: []byte("x")}, &docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo})

	res, err := svc.Backup(t.Context(), user, doc.ID)

	require.NoError(t, err)
	require.True(t, res.Noop)
	require.Equal(t, []string{"trash:remote-9"}, calls)
}

// renamingDrive runs a hook while the upload is in flight.
type renamingDrive struct {
	docsbottest.FakeDrive
	onUpload func()
}

func (d renamingDrive) Upload(ctx context.Context, user docsbot.UserID, in docsbot.DriveUpload) (docsbot.DriveFile, error) {
	d.onUpload()
	return d.FakeDrive.Upload(ctx, user, in)
}
