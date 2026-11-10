package gdrive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/oauth"
)

// syncDrive is a minimal Drive stub for Rename/Move/Trash/Replace.
type syncDrive struct {
	fileStatus int               // status for GET/PATCH of the file (0 = 200)
	name       string            // current file name
	parents    []string          // current file parents
	taken      map[string]string // folder id + "/" + name -> other file id
	folderID   string
	trashed    bool // the file sits in the Drive trash (still answers 200)

	goneFolders map[string]bool // ids that answer 404 on GET (stale cache)

	patches []patchCall
	listQs  []string
}

type patchCall struct {
	path, query, body, ctype, rawPath string
}

func (d *syncDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet:
		q := r.URL.Query().Get("q")
		d.listQs = append(d.listQs, q)
		switch {
		case strings.Contains(q, "mimeType='application/vnd.google-apps.folder'"):
			_, _ = io.WriteString(w, `{"files":[{"id":"`+d.folderID+`"}]}`)
		default:
			out := `{"files":[]}`
			for k, id := range d.taken {
				if strings.Contains(q, "name='"+strings.SplitN(k, "/", 2)[1]+"'") && strings.Contains(q, "'"+strings.SplitN(k, "/", 2)[0]+"' in parents") {
					out = `{"files":[{"id":"` + id + `"}]}`
				}
			}
			_, _ = io.WriteString(w, out)
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/drive/v3/files/"):
		if d.goneFolders[strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")] {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if d.fileStatus != 0 {
			w.WriteHeader(d.fileStatus)
			return
		}
		b, _ := json.Marshal(map[string]any{"name": d.name, "parents": d.parents, "trashed": d.trashed})
		_, _ = w.Write(b)
	case r.Method == http.MethodPatch:
		body, _ := io.ReadAll(r.Body)
		d.patches = append(d.patches, patchCall{r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Content-Type"), r.URL.EscapedPath()})
		if d.fileStatus != 0 {
			w.WriteHeader(d.fileStatus)
			return
		}
		_, _ = io.WriteString(w, `{"id":"f1"}`)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func serve(t *testing.T, d *syncDrive) string {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return srv.URL
}

func newSyncSvc(t *testing.T, d *syncDrive, clients Clients) *Service {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return New(clients, newFakeCache(), WithBaseURL(srv.URL))
}

func TestRename(t *testing.T) {
	tests := []struct {
		name        string
		drive       *syncDrive
		wantErr     error
		wantPatches int
		wantBody    string
	}{
		{"renames", &syncDrive{name: "old.pdf", parents: []string{"p1"}}, nil, 1, `{"name":"new.pdf"}`},
		{"same name is no-op", &syncDrive{name: "new.pdf", parents: []string{"p1"}}, nil, 0, ""},
		{"collision gets suffix", &syncDrive{name: "old.pdf", parents: []string{"p1"}, taken: map[string]string{"p1/new.pdf": "other"}}, nil, 1, `{"name":"new.pdf (1)"}`},
		{"own name is not a collision", &syncDrive{name: "new.pdf", parents: []string{"p1"}, taken: map[string]string{"p1/new.pdf": "f1"}}, nil, 0, ""},
		{"missing file", &syncDrive{fileStatus: http.StatusNotFound}, ErrFileNotFound, 0, ""},
		{"revoked", &syncDrive{fileStatus: http.StatusUnauthorized}, ErrAccessRevoked, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSyncSvc(t, tt.drive, fakeClients{})
			err := svc.Rename(context.Background(), "u1", "f1", "new.pdf")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, tt.drive.patches, tt.wantPatches)
			if tt.wantPatches > 0 {
				require.JSONEq(t, tt.wantBody, tt.drive.patches[0].body)
			}
		})
	}
}

func TestRename_ServerErrorAndOAuth(t *testing.T) {
	d := &syncDrive{fileStatus: http.StatusServiceUnavailable}
	err := newSyncSvc(t, d, fakeClients{}).Rename(context.Background(), "u1", "f1", "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrFileNotFound)
	require.NotErrorIs(t, err, ErrAccessRevoked)

	err = newSyncSvc(t, &syncDrive{}, fakeClients{err: oauth.ErrAccessRevoked}).Rename(context.Background(), "u1", "f1", "x")
	require.ErrorIs(t, err, ErrAccessRevoked)
}

func TestMove(t *testing.T) {
	tests := []struct {
		name        string
		drive       *syncDrive
		wantErr     error
		wantPatches int
		wantQuery   []string
	}{
		{"moves", &syncDrive{parents: []string{"old"}, folderID: "target"}, nil, 1, []string{"addParents=target", "removeParents=old"}},
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

func TestTrash(t *testing.T) {
	d := &syncDrive{}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Trash(context.Background(), "u1", "f1"))
	require.Len(t, d.patches, 1)
	require.Equal(t, "/drive/v3/files/f1", d.patches[0].path)
	require.JSONEq(t, `{"trashed":true}`, d.patches[0].body)

	d = &syncDrive{fileStatus: http.StatusNotFound}
	require.ErrorIs(t, newSyncSvc(t, d, fakeClients{}).Trash(context.Background(), "u1", "f1"), ErrFileNotFound)

	d = &syncDrive{fileStatus: http.StatusTooManyRequests}
	err := newSyncSvc(t, d, fakeClients{}).Trash(context.Background(), "u1", "f1")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrFileNotFound)
}

func TestReplace(t *testing.T) {
	d := &syncDrive{}
	require.NoError(t, newSyncSvc(t, d, fakeClients{}).Replace(context.Background(), "u1", "f1", "application/pdf", []byte("PDFDATA")))
	require.Len(t, d.patches, 1)
	p := d.patches[0]
	require.Equal(t, "/upload/drive/v3/files/f1", p.path)
	require.Contains(t, p.query, "uploadType=multipart")
	require.Contains(t, p.ctype, "multipart/related")
	require.Contains(t, p.body, "PDFDATA")
	require.Contains(t, p.body, "application/pdf")

	d = &syncDrive{fileStatus: http.StatusNotFound}
	require.ErrorIs(t, newSyncSvc(t, d, fakeClients{}).Replace(context.Background(), "u1", "f1", "", nil), ErrFileNotFound)

	d = &syncDrive{fileStatus: http.StatusUnauthorized}
	require.ErrorIs(t, newSyncSvc(t, d, fakeClients{}).Replace(context.Background(), "u1", "f1", "", nil), ErrAccessRevoked)
}
