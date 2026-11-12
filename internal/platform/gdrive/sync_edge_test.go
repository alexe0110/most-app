package gdrive

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type syncOp struct {
	name string
	call func(s *Service) error
}

func syncOps() []syncOp {
	ctx := context.Background()
	return []syncOp{
		{"rename", func(s *Service) error { return s.Rename(ctx, "u1", "f1", "new.pdf") }},
		{"move", func(s *Service) error { return s.Move(ctx, "u1", "f1", "passport", "Паспорт") }},
		{"trash", func(s *Service) error { return s.Trash(ctx, "u1", "f1") }},
		{"replace", func(s *Service) error { return s.Replace(ctx, "u1", "f1", "application/pdf", []byte("x")) }},
	}
}

// Every sync operation maps HTTP statuses the same way: 401 is a revoked
// access, 404 a missing file, anything else a plain retryable error.
func TestSyncOps_statusMapping(t *testing.T) {
	tests := []struct {
		status      int
		revoked     bool
		notFound    bool
		wantPlainEr bool
	}{
		{status: http.StatusUnauthorized, revoked: true},
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusTooManyRequests, wantPlainEr: true},
		{status: http.StatusInternalServerError, wantPlainEr: true},
		{status: http.StatusBadGateway, wantPlainEr: true},
		{status: http.StatusForbidden, wantPlainEr: true},
	}
	for _, op := range syncOps() {
		for _, tt := range tests {
			t.Run(op.name+"/"+http.StatusText(tt.status), func(t *testing.T) {
				d := &syncDrive{fileStatus: tt.status, name: "old.pdf", parents: []string{"p1"}, folderID: "target"}
				err := op.call(newSyncSvc(t, d, fakeClients{}))
				require.Error(t, err)
				require.Equal(t, tt.revoked, errors.Is(err, ErrAccessRevoked))
				require.Equal(t, tt.notFound, errors.Is(err, ErrFileNotFound))
			})
		}
	}
}

func TestSyncOps_revokedOAuth(t *testing.T) {
	for _, op := range syncOps() {
		t.Run(op.name, func(t *testing.T) {
			d := &syncDrive{}
			err := op.call(newSyncSvc(t, d, fakeClients{err: ErrAccessRevoked}))
			require.ErrorIs(t, err, ErrAccessRevoked)
			require.Empty(t, d.patches)
		})
	}
}

func TestSyncOps_cancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &syncDrive{name: "old.pdf", parents: []string{"p1"}}
	svc := newSyncSvc(t, d, fakeClients{})
	require.Error(t, svc.Rename(ctx, "u1", "f1", "new.pdf"))
	require.Error(t, svc.Trash(ctx, "u1", "f1"))
	require.Error(t, svc.Replace(ctx, "u1", "f1", "", nil))
	require.Empty(t, d.patches)
}

func TestRename_collisionChain(t *testing.T) {
	// "new.pdf" and "new (1).pdf" are taken by other files: pick (2).
	d := &syncDrive{name: "old.pdf", parents: []string{"p1"}, taken: map[string]string{
		"p1/new.pdf": "other1", "p1/new (1).pdf": "other2",
	}}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", "new.pdf"))
	require.Len(t, d.patches, 1)
	require.JSONEq(t, `{"name":"new (2).pdf"}`, d.patches[0].body)
}

func TestRename_alreadySuffixedIsIdempotent(t *testing.T) {
	// The file already carries "new (1).pdf" because "new.pdf" is another file's:
	// a repeated sync must not rename again.
	d := &syncDrive{name: "new (1).pdf", parents: []string{"p1"}, taken: map[string]string{
		"p1/new.pdf": "other", "p1/new (1).pdf": "f1",
	}}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", "new.pdf"))
	require.Empty(t, d.patches)
}

func TestRename_quoteInNameDoesNotBreakQuery(t *testing.T) {
	d := &syncDrive{name: "old.pdf", parents: []string{"p1"}}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", `it's.pdf`))
	require.Len(t, d.patches, 1)
	require.Contains(t, d.listQs[0], `it\'s.pdf`)
}

func TestMove_multipleParentsAreAllRemoved(t *testing.T) {
	d := &syncDrive{parents: []string{"a", "b"}, folderID: "target"}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.Len(t, d.patches, 1)
	require.Contains(t, d.patches[0].query, "addParents=target")
	require.Contains(t, d.patches[0].query, "removeParents=a%2Cb")
}

func TestMove_rootFolder(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		wantRoot string
	}{
		{"default", nil, `name='Most Docs'`},
		{"custom", []Option{WithRootFolder("Most Docs Test")}, `name='Most Docs Test'`},
		{"empty keeps default", []Option{WithRootFolder("")}, `name='Most Docs'`},
		{"apostrophe is escaped", []Option{WithRootFolder("Bob's Docs")}, `name='Bob\'s Docs'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &syncDrive{parents: []string{"old"}, folderID: "target"}
			svc := newSyncSvc(t, d, fakeClients{}, tt.opts...)

			require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))

			require.NotEmpty(t, d.listQs)
			require.Contains(t, d.listQs[0], tt.wantRoot, "first lookup is the root folder")
			require.Contains(t, d.listQs[0], "'root' in parents")
			require.Len(t, d.patches, 1)
			require.Contains(t, d.patches[0].query, "addParents=target")
		})
	}
}

func TestMove_twiceIsIdempotent(t *testing.T) {
	d := &syncDrive{parents: []string{"target"}, folderID: "target"}
	svc := newSyncSvc(t, d, fakeClients{})
	require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.Empty(t, d.patches)
}

func TestMove_staleCachedFolderIsRecreated(t *testing.T) {
	// The cache points at a folder Drive no longer has: Move drops it, resolves a
	// fresh one and moves the file there.
	d := &syncDrive{parents: []string{"old"}, folderID: "fresh", goneFolders: map[string]bool{"stale": true}}
	cache := newFakeCache()
	cache.ids["passport"] = "stale"
	svc := New(fakeClients{}, cache, WithBaseURL(serve(t, d)))

	require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.Contains(t, cache.deleted, "passport")
	require.Equal(t, "fresh", cache.ids["passport"])
	require.Len(t, d.patches, 1)
	require.Contains(t, d.patches[0].query, "addParents=fresh")
}

func TestReplace_targetsSameFileAndKeepsMetadata(t *testing.T) {
	d := &syncDrive{}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Replace(context.Background(), "u1", "f/1", "image/png", []byte("PNG")))
	require.Len(t, d.patches, 1)
	require.Equal(t, "/upload/drive/v3/files/f%2F1", d.patches[0].rawPath)
	// No name or parents in the metadata part: only media changes.
	require.NotContains(t, d.patches[0].body, `"name"`)
	require.NotContains(t, d.patches[0].body, "parents")
	require.Contains(t, d.patches[0].body, "image/png")
}

// A user who deletes the file in the Drive UI sends it to the trash, where
// files.get still answers 200. The sync must report it as gone (ErrFileNotFound)
// instead of renaming or replacing a trashed file and calling it a success.
func TestSyncOps_userTrashedFileIsMissing(t *testing.T) {
	for _, op := range syncOps() {
		if op.name == "trash" {
			continue
		}
		t.Run(op.name, func(t *testing.T) {
			d := &syncDrive{name: "old.pdf", parents: []string{"p1"}, folderID: "target", trashed: true}
			err := op.call(newSyncSvc(t, d, fakeClients{}))
			require.ErrorIs(t, err, ErrFileNotFound)
			require.Empty(t, d.patches, "a trashed file is left alone")
		})
	}
}

func TestRename_collisionWithoutExtension(t *testing.T) {
	d := &syncDrive{name: "old", parents: []string{"p1"}, taken: map[string]string{"p1/new": "other"}}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", "new"))
	require.JSONEq(t, `{"name":"new (1)"}`, d.patches[0].body)
}
