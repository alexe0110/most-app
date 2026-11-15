package gdrive

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/oauth"
)

func TestRename(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		drive       *syncDrive
		to          string // new name; "new.pdf" when empty
		wantErr     error
		wantPatches int
		wantBody    string
		wantQuery   string // substring expected in the first name lookup
	}{
		{name: "renames", drive: &syncDrive{name: "old.pdf", parents: []string{"p1"}}, wantPatches: 1, wantBody: `{"name":"new.pdf"}`},
		{name: "same name is no-op", drive: &syncDrive{name: "new.pdf", parents: []string{"p1"}}},
		{
			name:        "collision gets suffix",
			drive:       &syncDrive{name: "old.pdf", parents: []string{"p1"}, taken: map[string]string{"p1/new.pdf": "other"}},
			wantPatches: 1, wantBody: `{"name":"new (1).pdf"}`,
		},
		{
			// "new.pdf" and "new (1).pdf" are taken by other files: pick (2).
			name: "collision chain",
			drive: &syncDrive{name: "old.pdf", parents: []string{"p1"}, taken: map[string]string{
				"p1/new.pdf": "other1", "p1/new (1).pdf": "other2",
			}},
			wantPatches: 1, wantBody: `{"name":"new (2).pdf"}`,
		},
		{
			name:  "own name is not a collision",
			drive: &syncDrive{name: "new.pdf", parents: []string{"p1"}, taken: map[string]string{"p1/new.pdf": "f1"}},
		},
		{
			// The file already carries "new (1).pdf" because "new.pdf" is another file's:
			// a repeated sync must not rename again.
			name: "already suffixed is idempotent",
			drive: &syncDrive{name: "new (1).pdf", parents: []string{"p1"}, taken: map[string]string{
				"p1/new.pdf": "other", "p1/new (1).pdf": "f1",
			}},
		},
		{
			name:  "collision without extension",
			drive: &syncDrive{name: "old", parents: []string{"p1"}, taken: map[string]string{"p1/new": "other"}},
			to:    "new", wantPatches: 1, wantBody: `{"name":"new (1)"}`,
		},
		{
			name:  "quote in name does not break query",
			drive: &syncDrive{name: "old.pdf", parents: []string{"p1"}},
			to:    `it's.pdf`, wantPatches: 1, wantBody: `{"name":"it's.pdf"}`, wantQuery: `it\'s.pdf`,
		},
		{name: "missing file", drive: &syncDrive{fileStatus: http.StatusNotFound}, wantErr: ErrFileNotFound},
		{name: "revoked", drive: &syncDrive{fileStatus: http.StatusUnauthorized}, wantErr: ErrAccessRevoked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			to := tt.to
			if to == "" {
				to = "new.pdf"
			}
			svc := newSyncSvc(t, tt.drive, fakeClients{})
			err := svc.Rename(context.Background(), "u1", "f1", to)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, tt.drive.patches, tt.wantPatches)
			if tt.wantPatches > 0 {
				require.JSONEq(t, tt.wantBody, tt.drive.patches[0].body)
			}
			if tt.wantQuery != "" {
				require.NotEmpty(t, tt.drive.listQs)
				require.Contains(t, tt.drive.listQs[0], tt.wantQuery)
			}
		})
	}
}

func TestRename_ServerErrorAndOAuth(t *testing.T) {
	t.Parallel()
	d := &syncDrive{fileStatus: http.StatusServiceUnavailable}
	err := newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrFileNotFound)
	require.NotErrorIs(t, err, ErrAccessRevoked)

	err = newSyncSvc(t, &syncDrive{}, fakeClients{err: oauth.ErrAccessRevoked}).Rename(context.Background(), "u1", "f1", "x")
	require.ErrorIs(t, err, ErrAccessRevoked)
}
