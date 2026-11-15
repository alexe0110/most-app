package gdrive

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMove(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		drive       *syncDrive
		wantErr     error
		wantPatches int
		wantQuery   []string
	}{
		{"moves", &syncDrive{parents: []string{"old"}, folderID: "target"}, nil, 1, []string{"addParents=target", "removeParents=old"}},
		{"all parents are removed", &syncDrive{parents: []string{"a", "b"}, folderID: "target"}, nil, 1, []string{"addParents=target", "removeParents=a%2Cb"}},
		{"already there", &syncDrive{parents: []string{"target"}, folderID: "target"}, nil, 0, nil},
		{"missing file", &syncDrive{fileStatus: http.StatusNotFound, folderID: "target"}, ErrFileNotFound, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSyncSvc(t, tt.drive, fakeClients{})
			err := svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, tt.drive.patches, tt.wantPatches)
			for _, q := range tt.wantQuery {
				require.Contains(t, tt.drive.patches[0].query, q)
			}
		})
	}
}

func TestMove_rootFolder(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	d := &syncDrive{parents: []string{"target"}, folderID: "target"}
	svc := newSyncSvc(t, d, fakeClients{})
	require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.NoError(t, svc.Move(context.Background(), "u1", "f1", "passport", "Паспорт"))
	require.Empty(t, d.patches)
}

func TestMove_staleCachedFolderIsRecreated(t *testing.T) {
	t.Parallel()
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
