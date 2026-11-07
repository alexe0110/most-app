package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/alexe0110/most-app/internal/platform/oauth"
)

const (
	rootFolderName = "Most Docs"
	defaultBaseURL = "https://www.googleapis.com"
	folderMIME     = "application/vnd.google-apps.folder"
	maxNameTries   = 50
	maxErrBody     = 2 << 10
)

// Clients hands out an authenticated *http.Client for a user. Token refresh and
// revoke detection live in platform/oauth; declared here, implemented in wiring.
type Clients interface {
	Client(ctx context.Context, userID string) (*http.Client, error)
}

// FolderCache caches resolved Drive folder ids per (user, cacheKey). Declared
// here, implemented by the bot's postgres adapter over drive_folders.
type FolderCache interface {
	FolderID(ctx context.Context, userID, cacheKey string) (id string, ok bool, err error)
	SaveFolderID(ctx context.Context, userID, cacheKey, id string) error
	DeleteFolderID(ctx context.Context, userID, cacheKey string) error
}

// ErrAccessRevoked means Google rejected the grant (revoked / 401). The caller
// translates it into its own domain meaning and should not retry.
var ErrAccessRevoked = errors.New("gdrive: access revoked")

// errNotFound is an internal signal for a 404 response.
var errNotFound = errors.New("gdrive: not found")

// Service uploads files to the user's Drive. Construct it with New.
type Service struct {
	clients Clients
	cache   FolderCache
	baseURL string
}

// Option overrides a Service default.
type Option func(*Service)

// WithBaseURL overrides the Google API base (scheme+host, no trailing slash) for tests.
func WithBaseURL(u string) Option { return func(s *Service) { s.baseURL = u } }

// New builds the Service.
func New(clients Clients, cache FolderCache, opts ...Option) *Service {
	s := &Service{clients: clients, cache: cache, baseURL: defaultBaseURL}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Upload is a file to put into "Most Docs/<FolderName>".
type Upload struct {
	CacheKey   string // stable key for FolderCache (the category slug)
	FolderName string // human folder name under "Most Docs" (e.g. "Паспорт и ID")
	Name       string // desired file name
	MIME       string // content type of Data (may be "")
	Data       []byte
}

// File is an uploaded Drive file.
type File struct {
	ID   string
	Link string
}

// Upload stores the file in the user's category folder and returns its link.
func (s *Service) Upload(ctx context.Context, userID string, up Upload) (File, error) {
	httpc, err := s.clients.Client(ctx, userID)
	if err != nil {
		if errors.Is(err, oauth.ErrAccessRevoked) {
			return File{}, ErrAccessRevoked
		}
		return File{}, fmt.Errorf("drive client: %w", err)
	}

	rootID, err := s.ensureFolder(ctx, httpc, userID, "", rootFolderName, "root")
	if err != nil {
		return File{}, fmt.Errorf("ensure root folder: %w", err)
	}
	folderID, err := s.ensureFolder(ctx, httpc, userID, up.CacheKey, up.FolderName, rootID)
	if err != nil {
		return File{}, fmt.Errorf("ensure category folder: %w", err)
	}

	name, err := s.uniqueName(ctx, httpc, folderID, up.Name)
	if err != nil {
		return File{}, fmt.Errorf("pick file name: %w", err)
	}
	return s.upload(ctx, httpc, folderID, name, up)
}

// ensureFolder resolves a folder id: cache, then lookup by name, then create.
// An empty cacheKey skips the cache.
func (s *Service) ensureFolder(ctx context.Context, httpc *http.Client, userID, cacheKey, name, parentID string) (string, error) {
	if cacheKey != "" {
		id, ok, err := s.cache.FolderID(ctx, userID, cacheKey)
		if err != nil {
			return "", fmt.Errorf("read folder cache: %w", err)
		}
		if ok {
			alive, err := s.folderAlive(ctx, httpc, id)
			if err != nil {
				return "", err
			}
			if alive {
				return id, nil
			}
			if err := s.cache.DeleteFolderID(ctx, userID, cacheKey); err != nil {
				return "", fmt.Errorf("reset folder cache: %w", err)
			}
		}
	}

	id, err := s.findFolder(ctx, httpc, name, parentID)
	if err != nil {
		return "", err
	}
	if id == "" {
		id, err = s.createFolder(ctx, httpc, name, parentID)
		if err != nil {
			return "", err
		}
	}
	if cacheKey != "" {
		if err := s.cache.SaveFolderID(ctx, userID, cacheKey, id); err != nil {
			return "", fmt.Errorf("save folder cache: %w", err)
		}
	}
	return id, nil
}

// folderAlive reports whether a cached folder still exists and is not trashed.
func (s *Service) folderAlive(ctx context.Context, httpc *http.Client, id string) (bool, error) {
	var out struct {
		Trashed bool `json:"trashed"`
	}
	u := s.baseURL + "/drive/v3/files/" + url.PathEscape(id) + "?fields=id,trashed"
	err := do(ctx, httpc, http.MethodGet, u, "", nil, &out)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("drive get folder: %w", err)
	}
	return !out.Trashed, nil
}

func (s *Service) findFolder(ctx context.Context, httpc *http.Client, name, parentID string) (string, error) {
	q := fmt.Sprintf("name='%s' and mimeType='%s' and '%s' in parents and trashed=false",
		escapeQuery(name), folderMIME, parentID)
	files, err := s.list(ctx, httpc, q)
	if err != nil {
		return "", fmt.Errorf("drive find folder: %w", err)
	}
	if len(files) == 0 {
		return "", nil
	}
	return files[0], nil
}

func (s *Service) createFolder(ctx context.Context, httpc *http.Client, name, parentID string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"mimeType": folderMIME,
		"parents":  []string{parentID},
	})
	if err != nil {
		return "", fmt.Errorf("encode folder: %w", err)
	}
	var out struct {
		ID string `json:"id"`
	}
	err = do(ctx, httpc, http.MethodPost, s.baseURL+"/drive/v3/files?fields=id",
		"application/json; charset=UTF-8", bytes.NewReader(body), &out)
	if err != nil {
		return "", fmt.Errorf("drive create folder: %w", err)
	}
	return out.ID, nil
}

// uniqueName appends " (n)" until no non-trashed file in the folder has the name.
func (s *Service) uniqueName(ctx context.Context, httpc *http.Client, folderID, name string) (string, error) {
	candidate := name
	for i := 1; i <= maxNameTries; i++ {
		q := fmt.Sprintf("name='%s' and '%s' in parents and trashed=false", escapeQuery(candidate), folderID)
		files, err := s.list(ctx, httpc, q)
		if err != nil {
			return "", fmt.Errorf("drive check name: %w", err)
		}
		if len(files) == 0 {
			return candidate, nil
		}
		candidate = name + " (" + strconv.Itoa(i) + ")"
	}
	return candidate, nil
}

// list runs a files.list query and returns the matching ids.
func (s *Service) list(ctx context.Context, httpc *http.Client, q string) ([]string, error) {
	v := url.Values{}
	v.Set("q", q)
	v.Set("fields", "files(id,name)")
	v.Set("spaces", "drive")

	var out struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := do(ctx, httpc, http.MethodGet, s.baseURL+"/drive/v3/files?"+v.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Files))
	for _, f := range out.Files {
		ids = append(ids, f.ID)
	}
	return ids, nil
}

// upload sends the file as multipart/related: JSON metadata, then the bytes.
func (s *Service) upload(ctx context.Context, httpc *http.Client, folderID, name string, up Upload) (File, error) {
	meta, err := json.Marshal(map[string]any{"name": name, "parents": []string{folderID}})
	if err != nil {
		return File{}, fmt.Errorf("encode metadata: %w", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	metaHdr := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
	if err := writePart(mw, metaHdr, meta); err != nil {
		return File{}, err
	}
	dataHdr := textproto.MIMEHeader{}
	if up.MIME != "" {
		dataHdr.Set("Content-Type", up.MIME)
	}
	if err := writePart(mw, dataHdr, up.Data); err != nil {
		return File{}, err
	}
	if err := mw.Close(); err != nil {
		return File{}, fmt.Errorf("close multipart: %w", err)
	}

	var out struct {
		ID          string `json:"id"`
		WebViewLink string `json:"webViewLink"`
	}
	u := s.baseURL + "/upload/drive/v3/files?uploadType=multipart&fields=id,webViewLink"
	ct := "multipart/related; boundary=" + mw.Boundary()
	if err := do(ctx, httpc, http.MethodPost, u, ct, &buf, &out); err != nil {
		return File{}, fmt.Errorf("drive upload: %w", err)
	}

	link := out.WebViewLink
	if link == "" {
		link = "https://drive.google.com/file/d/" + out.ID + "/view"
	}
	return File{ID: out.ID, Link: link}, nil
}

func writePart(mw *multipart.Writer, hdr textproto.MIMEHeader, data []byte) error {
	w, err := mw.CreatePart(hdr)
	if err != nil {
		return fmt.Errorf("create multipart part: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write multipart part: %w", err)
	}
	return nil
}

// do performs one request and decodes a JSON response into out. 401 maps to
// ErrAccessRevoked and 404 to errNotFound; other non-2xx carry only the status.
func do(ctx context.Context, httpc *http.Client, method, u, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrAccessRevoked
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		// Drain a bounded part so the connection can be reused; the body is not
		// put into the error because it may carry file metadata.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// escapeQuery escapes a literal for use inside single quotes in a Drive query.
func escapeQuery(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `'`, `\'`)
}
