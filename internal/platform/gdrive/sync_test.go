package gdrive

import (
	"context"
	"encoding/json"
	"errors"
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

func newSyncSvc(t *testing.T, d *syncDrive, clients Clients, opts ...Option) *Service {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return New(clients, newFakeCache(), append([]Option{WithBaseURL(srv.URL)}, opts...)...)
}

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &syncDrive{name: "old.pdf", parents: []string{"p1"}}
	svc := newSyncSvc(t, d, fakeClients{})
	require.Error(t, svc.Rename(ctx, "u1", "f1", "new.pdf"))
	require.Error(t, svc.Trash(ctx, "u1", "f1"))
	require.Error(t, svc.Replace(ctx, "u1", "f1", "", nil))
	require.Empty(t, d.patches)
}
func TestTrash(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestExists(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		drive   *syncDrive
		clients Clients
		want    bool
		wantErr error
		anyErr  bool // some error other than a missing file is expected
	}{
		{"alive", &syncDrive{}, fakeClients{}, true, nil, false},
		{"trashed", &syncDrive{trashed: true}, fakeClients{}, false, nil, false},
		{"404", &syncDrive{fileStatus: http.StatusNotFound}, fakeClients{}, false, nil, false},
		{"401 is revoked", &syncDrive{fileStatus: http.StatusUnauthorized}, fakeClients{}, false, ErrAccessRevoked, false},
		{"403 is an error", &syncDrive{fileStatus: http.StatusForbidden}, fakeClients{}, false, nil, true},
		{"500 is an error", &syncDrive{fileStatus: http.StatusInternalServerError}, fakeClients{}, false, nil, true},
		{"oauth revoked", &syncDrive{}, fakeClients{err: oauth.ErrAccessRevoked}, false, ErrAccessRevoked, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := newSyncSvc(t, tt.drive, tt.clients).Exists(context.Background(), "u1", "f1")
			switch {
			case tt.anyErr:
				require.Error(t, err)
				require.NotErrorIs(t, err, ErrFileNotFound)
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			default:
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
			require.Empty(t, tt.drive.patches, "a check never writes")
		})
	}
}

func TestReplace_targetsSameFileAndKeepsMetadata(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
