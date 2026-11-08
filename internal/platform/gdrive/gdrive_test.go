package gdrive

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/oauth"
)

type fakeClients struct{ err error }

func (f fakeClients) Client(context.Context, string) (*http.Client, error) {
	if f.err != nil {
		return nil, f.err
	}
	return http.DefaultClient, nil
}

type fakeCache struct {
	ids     map[string]string
	saved   []string
	deleted []string
}

func newFakeCache() *fakeCache { return &fakeCache{ids: map[string]string{}} }

func (c *fakeCache) FolderID(_ context.Context, _, key string) (string, bool, error) {
	id, ok := c.ids[key]
	return id, ok, nil
}

func (c *fakeCache) SaveFolderID(_ context.Context, _, key, id string) error {
	c.ids[key] = id
	c.saved = append(c.saved, key)
	return nil
}

func (c *fakeCache) DeleteFolderID(_ context.Context, _, key string) error {
	delete(c.ids, key)
	c.deleted = append(c.deleted, key)
	return nil
}

// fakeDrive emulates the slice of the Drive API the client uses.
type fakeDrive struct {
	t            *testing.T
	gone         map[string]int      // folder id -> status for files/{id} (404)
	trashed      map[string]bool     // folder id -> trashed
	existingName string              // a file name already present in the category folder
	folders      []map[string]string // folders Drive already has, oldest first (as orderBy=createdTime returns them)
	lastOrderBy  string              // orderBy of the last folder query
	webViewLink  string
	created      []string // folder names created, in order
	uploadedName string
	uploadedBody string
	uploadedMIME string
	uploadParent string
}

func (d *fakeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/drive/v3/files/"):
		id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
		if _, ok := d.gone[id]; ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"id": id, "trashed": d.trashed[id]})
	case r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files":
		q := r.URL.Query().Get("q")
		files := []map[string]string{}
		if len(d.folders) > 0 && strings.Contains(q, "mimeType='"+folderMIME+"'") {
			d.lastOrderBy = r.URL.Query().Get("orderBy")
			for _, f := range d.folders {
				if strings.Contains(q, "name='"+f["name"]+"'") {
					files = append(files, f)
				}
			}
			writeJSON(w, map[string]any{"files": files})
			return
		}
		if d.existingName != "" && strings.Contains(q, "name='"+d.existingName+"'") &&
			!strings.Contains(q, "mimeType=") {
			files = append(files, map[string]string{"id": "existing", "name": d.existingName})
		}
		writeJSON(w, map[string]any{"files": files})
	case r.Method == http.MethodPost && r.URL.Path == "/drive/v3/files":
		var in struct {
			Name string `json:"name"`
		}
		require.NoError(d.t, json.NewDecoder(r.Body).Decode(&in))
		d.created = append(d.created, in.Name)
		writeJSON(w, map[string]string{"id": "folder-" + in.Name})
	case r.Method == http.MethodPost && r.URL.Path == "/upload/drive/v3/files":
		d.readUpload(r)
		resp := map[string]string{"id": "file-1"}
		if d.webViewLink != "" {
			resp["webViewLink"] = d.webViewLink
		}
		writeJSON(w, resp)
	default:
		d.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}
}

func (d *fakeDrive) readUpload(r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	require.NoError(d.t, err)
	mr := multipart.NewReader(r.Body, params["boundary"])

	meta, err := mr.NextPart()
	require.NoError(d.t, err)
	var m struct {
		Name    string   `json:"name"`
		Parents []string `json:"parents"`
	}
	require.NoError(d.t, json.NewDecoder(meta).Decode(&m))
	d.uploadedName = m.Name
	require.Len(d.t, m.Parents, 1)
	d.uploadParent = m.Parents[0]

	data, err := mr.NextPart()
	require.NoError(d.t, err)
	d.uploadedMIME = data.Header.Get("Content-Type")
	b, err := io.ReadAll(data)
	require.NoError(d.t, err)
	d.uploadedBody = string(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newService(t *testing.T, d *fakeDrive, clients Clients, cache FolderCache) *Service {
	t.Helper()
	d.t = t
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return New(clients, cache, WithBaseURL(srv.URL))
}

func testUpload() Upload {
	return Upload{CacheKey: "passport", FolderName: "Паспорт и ID", Name: "scan.pdf", MIME: "application/pdf", Data: []byte("pdf-bytes")}
}

func TestUpload_happyPath(t *testing.T) {
	d := &fakeDrive{webViewLink: "https://drive.google.com/file/d/file-1/view?usp=drivesdk"}
	cache := newFakeCache()
	svc := newService(t, d, fakeClients{}, cache)

	f, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, File{ID: "file-1", Link: d.webViewLink}, f)
	require.Equal(t, []string{rootFolderName, "Паспорт и ID"}, d.created)
	require.Equal(t, "folder-Паспорт и ID", d.uploadParent)
	require.Equal(t, "scan.pdf", d.uploadedName)
	require.Equal(t, "pdf-bytes", d.uploadedBody)
	require.Equal(t, "application/pdf", d.uploadedMIME)
	require.Equal(t, []string{"passport"}, cache.saved)
	require.Equal(t, "folder-Паспорт и ID", cache.ids["passport"])
}

func TestUpload_cachedFolderGone(t *testing.T) {
	tests := []struct {
		name string
		drv  *fakeDrive
	}{
		{name: "not found", drv: &fakeDrive{gone: map[string]int{"stale": 404}}},
		{name: "trashed", drv: &fakeDrive{trashed: map[string]bool{"stale": true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newFakeCache()
			cache.ids["passport"] = "stale"
			svc := newService(t, tt.drv, fakeClients{}, cache)

			_, err := svc.Upload(context.Background(), "user-1", testUpload())

			require.NoError(t, err)
			require.Equal(t, []string{"passport"}, cache.deleted)
			require.Contains(t, tt.drv.created, "Паспорт и ID", "folder must be recreated")
			require.Equal(t, "folder-Паспорт и ID", cache.ids["passport"])
		})
	}
}

func TestUpload_findFolderPicksOldest(t *testing.T) {
	d := &fakeDrive{folders: []map[string]string{
		{"id": "root", "name": rootFolderName},
		{"id": "old", "name": "Паспорт и ID"},
		{"id": "new", "name": "Паспорт и ID"},
	}}
	cache := newFakeCache()
	svc := newService(t, d, fakeClients{}, cache)

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "createdTime", d.lastOrderBy)
	require.Empty(t, d.created)
	require.Equal(t, "old", d.uploadParent)
	require.Equal(t, "old", cache.ids["passport"])
}

func TestUpload_cachedFolderAlive(t *testing.T) {
	d := &fakeDrive{}
	cache := newFakeCache()
	cache.ids["passport"] = "alive"
	svc := newService(t, d, fakeClients{}, cache)

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Empty(t, cache.deleted)
	require.Equal(t, "alive", d.uploadParent)
	require.NotContains(t, d.created, "Паспорт и ID")
}

func TestUpload_duplicateName(t *testing.T) {
	d := &fakeDrive{existingName: "scan.pdf"}
	svc := newService(t, d, fakeClients{}, newFakeCache())

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "scan.pdf (1)", d.uploadedName)
}

func TestUpload_revoked(t *testing.T) {
	svc := New(fakeClients{err: oauth.ErrAccessRevoked}, newFakeCache())

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.ErrorIs(t, err, ErrAccessRevoked)
}

func TestUpload_linkFallback(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())

	f, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "https://drive.google.com/file/d/file-1/view", f.Link)
}

func TestEscapeQuery(t *testing.T) {
	require.Equal(t, `it\'s`, escapeQuery("it's"))
}
