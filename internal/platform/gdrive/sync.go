package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// fileMeta is the part of a Drive file the sync operations care about.
type fileMeta struct {
	Name    string   `json:"name"`
	Parents []string `json:"parents"`
	Trashed bool     `json:"trashed"`
}

// Rename gives the file the name, adding " (n)" if another file in the same
// folder already has it. Renaming to the current name is a no-op.
func (s *Service) Rename(ctx context.Context, userID, fileID, name string) error {
	httpc, err := s.client(ctx, userID)
	if err != nil {
		return err
	}
	meta, err := s.getMeta(ctx, httpc, fileID)
	if err != nil {
		return err
	}
	final := name
	if len(meta.Parents) > 0 {
		final, err = s.uniqueName(ctx, httpc, meta.Parents[0], name, fileID)
		if err != nil {
			return fmt.Errorf("pick file name: %w", err)
		}
	}
	if final == meta.Name {
		return nil
	}
	return s.patch(ctx, httpc, "files.rename", fileID, "", map[string]any{"name": final})
}

// Move puts the file into "<root>/<folderName>", creating folders as needed.
// A file that is already there is left alone.
func (s *Service) Move(ctx context.Context, userID, fileID, cacheKey, folderName string) error {
	httpc, err := s.client(ctx, userID)
	if err != nil {
		return err
	}
	meta, err := s.getMeta(ctx, httpc, fileID)
	if err != nil {
		return err
	}

	target, err := s.resolveFolder(ctx, httpc, userID, cacheKey, folderName)
	if errors.Is(err, errNotFound) && cacheKey != "" {
		// A cached folder vanished between the alive-check and use: drop the
		// cache and resolve once more (same recovery as Upload).
		if derr := s.cache.DeleteFolderID(ctx, userID, cacheKey); derr != nil {
			return fmt.Errorf("reset folder cache: %w", derr)
		}
		target, err = s.resolveFolder(ctx, httpc, userID, cacheKey, folderName)
	}
	if err != nil {
		return err
	}

	if len(meta.Parents) == 1 && meta.Parents[0] == target {
		return nil
	}
	v := url.Values{}
	v.Set("addParents", target)
	if len(meta.Parents) > 0 {
		v.Set("removeParents", strings.Join(meta.Parents, ","))
	}
	return s.patch(ctx, httpc, "files.move", fileID, v.Encode(), map[string]any{})
}

// Exists reports whether the file is still in Drive: a 404 or a trashed file is
// gone (false, nil). A revoked grant or any other failure is an error, so the
// caller does not mistake an outage for a deleted file.
func (s *Service) Exists(ctx context.Context, userID, fileID string) (bool, error) {
	httpc, err := s.client(ctx, userID)
	if err != nil {
		return false, err
	}
	var meta struct {
		ID      string `json:"id"`
		Trashed bool   `json:"trashed"`
	}
	u := s.baseURL + "/drive/v3/files/" + url.PathEscape(fileID) + "?fields=id,trashed"
	err = s.do(ctx, "files.exists", httpc, http.MethodGet, u, "", nil, &meta)
	switch {
	case errors.Is(err, errNotFound):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("drive check file: %w", err)
	}
	return !meta.Trashed, nil
}

// Trash moves the file to the Drive trash (never a permanent delete).
func (s *Service) Trash(ctx context.Context, userID, fileID string) error {
	httpc, err := s.client(ctx, userID)
	if err != nil {
		return err
	}
	return s.patch(ctx, httpc, "files.trash", fileID, "", map[string]any{"trashed": true})
}

// Replace overwrites the content of the file, keeping its id, name and folder.
func (s *Service) Replace(ctx context.Context, userID, fileID, mime string, data []byte) error {
	httpc, err := s.client(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := s.getMeta(ctx, httpc, fileID); err != nil {
		return err
	}
	body, ct, err := multipartBody(map[string]any{}, mime, data)
	if err != nil {
		return err
	}
	u := s.baseURL + "/upload/drive/v3/files/" + url.PathEscape(fileID) + "?uploadType=multipart&fields=id"
	var out struct {
		ID string `json:"id"`
	}
	if err := s.do(ctx, "files.replace", httpc, http.MethodPatch, u, ct, body, &out); err != nil {
		return fileErr("drive replace", err)
	}
	return nil
}

func (s *Service) resolveFolder(ctx context.Context, httpc *http.Client, userID, cacheKey, folderName string) (string, error) {
	rootID, err := s.ensureFolder(ctx, httpc, userID, "", s.rootFolder, "root")
	if err != nil {
		return "", fmt.Errorf("ensure root folder: %w", err)
	}
	id, err := s.ensureFolder(ctx, httpc, userID, cacheKey, folderName, rootID)
	if err != nil {
		return "", fmt.Errorf("ensure category folder: %w", err)
	}
	return id, nil
}

func (s *Service) getMeta(ctx context.Context, httpc *http.Client, fileID string) (fileMeta, error) {
	var meta fileMeta
	u := s.baseURL + "/drive/v3/files/" + url.PathEscape(fileID) + "?fields=name,parents,trashed"
	if err := s.do(ctx, "files.get", httpc, http.MethodGet, u, "", nil, &meta); err != nil {
		return fileMeta{}, fileErr("drive get file", err)
	}
	if meta.Trashed {
		// Trashed in the Drive UI: the file is gone as far as we are concerned.
		return fileMeta{}, ErrFileNotFound
	}
	return meta, nil
}

// patch sends a JSON files.update; rawQuery is appended to the URL when set.
func (s *Service) patch(ctx context.Context, httpc *http.Client, op, fileID, rawQuery string, md map[string]any) error {
	body, err := json.Marshal(md)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}
	u := s.baseURL + "/drive/v3/files/" + url.PathEscape(fileID) + "?fields=id"
	if rawQuery != "" {
		u += "&" + rawQuery
	}
	var out struct {
		ID string `json:"id"`
	}
	err = s.do(ctx, op, httpc, http.MethodPatch, u, "application/json; charset=UTF-8", bytes.NewReader(body), &out)
	if err != nil {
		return fileErr("drive update file", err)
	}
	return nil
}

// fileErr maps the internal 404 signal to ErrFileNotFound and wraps the rest.
func fileErr(msg string, err error) error {
	if errors.Is(err, errNotFound) {
		return ErrFileNotFound
	}
	return fmt.Errorf("%s: %w", msg, err)
}
