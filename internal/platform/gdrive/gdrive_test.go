package gdrive

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	created      []string            // folder names created, in order
	uploadedName string
	uploadedBody string
	uploadedMIME string
	uploadParent string
	hasAppProps  bool         // last upload carried appProperties
	uploads      int          // number of upload POSTs
	stored       []storedFile // files created by uploads
	lastFileQ    string       // last appProperties query
}

type storedFile struct {
	id, docID, parent string
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
				if f["trashed"] == "true" && strings.Contains(q, "trashed=false") {
					continue // the query excludes trashed folders
				}
				if strings.Contains(q, "name='"+f["name"]+"'") {
					files = append(files, f)
				}
			}
			writeJSON(w, map[string]any{"files": files})
			return
		}
		if strings.Contains(q, "appProperties has") {
			d.lastFileQ = q
			for _, f := range d.stored {
				if strings.Contains(q, "value='"+f.docID+"'") && strings.Contains(q, "'"+f.parent+"' in parents") &&
					strings.Contains(q, "trashed=false") {
					files = append(files, map[string]string{"id": f.id})
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
		writeJSON(w, map[string]string{"id": "file-" + strconv.Itoa(len(d.stored))})
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
		Name          string            `json:"name"`
		Parents       []string          `json:"parents"`
		AppProperties map[string]string `json:"appProperties"`
	}
	require.NoError(d.t, json.NewDecoder(meta).Decode(&m))
	d.uploadedName = m.Name
	require.Len(d.t, m.Parents, 1)
	d.uploadParent = m.Parents[0]
	d.uploads++
	d.stored = append(d.stored, storedFile{
		id: "file-" + strconv.Itoa(len(d.stored)+1), docID: m.AppProperties["most_doc_id"], parent: m.Parents[0],
	})
	d.hasAppProps = m.AppProperties != nil

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

func newService(t *testing.T, d *fakeDrive, clients Clients, cache FolderCache, opts ...Option) *Service {
	t.Helper()
	d.t = t
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return New(clients, cache, append([]Option{WithBaseURL(srv.URL)}, opts...)...)
}

func testUpload() Upload {
	return Upload{CacheKey: "passport", FolderName: "Паспорт и ID", Name: "scan.pdf", MIME: "application/pdf", Data: []byte("pdf-bytes")}
}

func TestUpload_happyPath(t *testing.T) {
	d := &fakeDrive{}
	cache := newFakeCache()
	svc := newService(t, d, fakeClients{}, cache)

	f, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, File{ID: "file-1"}, f)
	require.Equal(t, []string{DefaultRootFolder, "Паспорт и ID"}, d.created)
	require.Equal(t, "folder-Паспорт и ID", d.uploadParent)
	require.Equal(t, "scan.pdf", d.uploadedName)
	require.Equal(t, "pdf-bytes", d.uploadedBody)
	require.Equal(t, "application/pdf", d.uploadedMIME)
	require.False(t, d.hasAppProps, "no appProperties without DocID")
	require.Equal(t, []string{"passport"}, cache.saved)
	require.Equal(t, "folder-Паспорт и ID", cache.ids["passport"])
}

func TestUpload_rootFolder(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		wantRoot string
	}{
		{name: "default", wantRoot: DefaultRootFolder},
		{name: "custom", opts: []Option{WithRootFolder("Most Docs Test")}, wantRoot: "Most Docs Test"},
		{name: "empty keeps default", opts: []Option{WithRootFolder("")}, wantRoot: DefaultRootFolder},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDrive{}
			svc := newService(t, d, fakeClients{}, newFakeCache(), tt.opts...)

			_, err := svc.Upload(context.Background(), "user-1", testUpload())

			require.NoError(t, err)
			require.Equal(t, []string{tt.wantRoot, "Паспорт и ID"}, d.created)
		})
	}
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
		{"id": "root", "name": DefaultRootFolder},
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

// Case (a): the root is in the trash, so the trashed=false search skips it; a new
// root is created, the category goes under it and the file is uploaded.
// Case (c) (root deleted permanently) is indistinguishable for the search: the
// root is simply absent, which TestUpload_happyPath already covers.
func TestUpload_trashedRootIsIgnored(t *testing.T) {
	d := &fakeDrive{folders: []map[string]string{
		{"id": "old-root", "name": DefaultRootFolder, "trashed": "true"},
	}}
	cache := newFakeCache()
	svc := newService(t, d, fakeClients{}, cache)

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, []string{DefaultRootFolder, "Паспорт и ID"}, d.created)
	require.Equal(t, "folder-Паспорт и ID", d.uploadParent)
	require.Equal(t, []string{"passport"}, cache.saved, "only the category is cached, never the root")
}

// Case (b): the cached category folder is trashed while the root is alive; the
// cache entry is dropped and the category is recreated under the same root.
func TestUpload_trashedCategoryKeepsRoot(t *testing.T) {
	d := &fakeDrive{
		trashed: map[string]bool{"stale": true},
		folders: []map[string]string{{"id": "live-root", "name": DefaultRootFolder}},
	}
	cache := newFakeCache()
	cache.ids["passport"] = "stale"
	svc := newService(t, d, fakeClients{}, cache)

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, []string{"passport"}, cache.deleted)
	require.Equal(t, []string{"Паспорт и ID"}, d.created, "root must not be recreated")
	require.Equal(t, "folder-Паспорт и ID", d.uploadParent)
}

// Case (e): two roots with the same name (one restored from the trash, one
// created later); the oldest wins and no new root is created.
func TestUpload_findFolderPicksOldestRoot(t *testing.T) {
	d := &fakeDrive{folders: []map[string]string{
		{"id": "old-root", "name": DefaultRootFolder},
		{"id": "new-root", "name": DefaultRootFolder},
	}}
	svc := newService(t, d, fakeClients{}, newFakeCache())

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "createdTime", d.lastOrderBy)
	require.Equal(t, []string{"Паспорт и ID"}, d.created, "no new root")
}

// Case (g): the user renamed or moved the folders; the cache is keyed by id, so
// the cached folder stays alive, no search runs and the upload goes to that id.
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
	require.Equal(t, "scan (1).pdf", d.uploadedName)
}

func TestUpload_revoked(t *testing.T) {
	svc := New(fakeClients{err: oauth.ErrAccessRevoked}, newFakeCache())

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.ErrorIs(t, err, ErrAccessRevoked)
}

func TestUpload_idempotentByDocID(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())
	up := testUpload()
	up.DocID = "doc-1"

	first, err := svc.Upload(context.Background(), "user-1", up)
	require.NoError(t, err)
	require.True(t, d.hasAppProps)
	require.Equal(t, "doc-1", d.stored[0].docID)

	second, err := svc.Upload(context.Background(), "user-1", up)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, 1, d.uploads)
	require.Contains(t, d.lastFileQ, "trashed=false")
	require.Contains(t, d.lastFileQ, "value='doc-1'")
}

func TestEscapeQuery(t *testing.T) {
	require.Equal(t, `it\'s`, escapeQuery("it's"))
}

// racyDrive wraps fakeDrive and answers 404 to child listings and uploads
// under a parent for which gone returns true, while GET files/{id} keeps
// reporting the folder as alive (the check-then-use race).
type racyDrive struct {
	*fakeDrive
	gone func(parent string) bool
}

func (d racyDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/drive/v3/files" {
		q := r.URL.Query().Get("q")
		for _, parent := range []string{"stale", "folder-Паспорт и ID"} {
			if d.gone(parent) && strings.Contains(q, "'"+parent+"' in parents") {
				http.NotFound(w, r)
				return
			}
		}
	}
	d.fakeDrive.ServeHTTP(w, r)
}

func newRacyService(t *testing.T, d racyDrive, cache FolderCache) *Service {
	t.Helper()
	d.t = t
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return New(fakeClients{}, cache, WithBaseURL(srv.URL))
}

func TestUpload_staleFolderRaceRetriesOnce(t *testing.T) {
	d := racyDrive{fakeDrive: &fakeDrive{}, gone: func(p string) bool { return p == "stale" }}
	cache := newFakeCache()
	cache.ids["passport"] = "stale"
	svc := newRacyService(t, d, cache)

	f, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "file-1", f.ID)
	require.Equal(t, []string{"passport"}, cache.deleted)
	require.Equal(t, 1, countOf(d.created, "Паспорт и ID"), "folder recreated exactly once")
	require.Equal(t, "folder-Паспорт и ID", d.uploadParent)
	require.Equal(t, "folder-Паспорт и ID", cache.ids["passport"])
	require.Equal(t, 1, d.uploads)
}

func TestUpload_staleFolderRacePersistentFails(t *testing.T) {
	d := racyDrive{fakeDrive: &fakeDrive{}, gone: func(string) bool { return true }}
	cache := newFakeCache()
	cache.ids["passport"] = "stale"
	svc := newRacyService(t, d, cache)

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.ErrorIs(t, err, errNotFound)
	require.Equal(t, []string{"passport"}, cache.deleted, "retry must not loop")
	require.Equal(t, 1, countOf(d.created, "Паспорт и ID"))
	require.Zero(t, d.uploads)
}

func countOf(items []string, want string) int {
	n := 0
	for _, it := range items {
		if it == want {
			n++
		}
	}
	return n
}

// An empty DocID (legacy callers) keeps the plain upload: no appProperties,
// no lookup by property.
func TestUpload_emptyDocIDSkipsIdempotency(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())

	f, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.NoError(t, err)
	require.Equal(t, "file-1", f.ID)
	require.False(t, d.hasAppProps)
	require.Empty(t, d.lastFileQ, "no appProperties lookup without a DocID")
	require.Equal(t, 1, d.uploads)
}

// Files of other documents, or in another folder, are not reused.
func TestUpload_docIDLookupIsScoped(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())
	a, b := testUpload(), testUpload()
	a.DocID, b.DocID = "doc-a", "doc-b"

	fa, err := svc.Upload(context.Background(), "user-1", a)
	require.NoError(t, err)
	fb, err := svc.Upload(context.Background(), "user-1", b)
	require.NoError(t, err)

	require.NotEqual(t, fa.ID, fb.ID)
	require.Equal(t, 2, d.uploads)

	other := a
	other.CacheKey, other.FolderName = "other", "Другое"
	fo, err := svc.Upload(context.Background(), "user-1", other)
	require.NoError(t, err)
	require.NotEqual(t, fa.ID, fo.ID, "same doc in another folder is a different upload")
}

// The lookup must not match a quote in the id as query syntax.
func TestUpload_docIDIsEscapedInQuery(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())
	up := testUpload()
	up.DocID = "x' or trashed=true or name='"

	_, err := svc.Upload(context.Background(), "user-1", up)

	require.NoError(t, err)
	require.Contains(t, d.lastFileQ, `value='x\' or trashed=true or name=\''`)
}

func TestUpload_canceledContext(t *testing.T) {
	d := &fakeDrive{}
	svc := newService(t, d, fakeClients{}, newFakeCache())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.Upload(ctx, "user-1", testUpload())

	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, d.uploads)
}

// The user disconnect maps to its own error, never to the revoke one.
func TestUpload_disconnectedByUser(t *testing.T) {
	svc := newService(t, &fakeDrive{}, fakeClients{err: oauth.ErrAccessDisconnected}, newFakeCache())

	_, err := svc.Upload(context.Background(), "user-1", testUpload())

	require.ErrorIs(t, err, ErrAccessDisconnected)
	require.NotErrorIs(t, err, ErrAccessRevoked)
}
