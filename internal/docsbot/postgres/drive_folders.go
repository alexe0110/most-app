package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
)

// This file implements gdrive.FolderCache over the drive_folders table; the
// cache key is stored in the category column.

var _ gdrive.FolderCache = (*Repo)(nil)

// FolderID returns the cached Drive folder id for (user, cacheKey).
func (r *Repo) FolderID(ctx context.Context, userID, cacheKey string) (string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return "", false, err
	}
	row, err := r.GetDriveFolder(ctx, GetDriveFolderParams{UserID: uid, Category: cacheKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get drive folder: %w", err)
	}
	return row.DriveFolderID, true, nil
}

// SaveFolderID stores (or replaces) the Drive folder id for (user, cacheKey).
func (r *Repo) SaveFolderID(ctx context.Context, userID, cacheKey, id string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if _, err := r.UpsertDriveFolder(ctx, UpsertDriveFolderParams{UserID: uid, Category: cacheKey, DriveFolderID: id}); err != nil {
		return fmt.Errorf("upsert drive folder: %w", err)
	}
	return nil
}

// DeleteAllFolders clears the user's cached Drive folder ids.
func (r *Repo) DeleteAllFolders(ctx context.Context, user docsbot.UserID) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	if err := r.DeleteUserDriveFolders(ctx, uid); err != nil {
		return fmt.Errorf("delete user drive folders: %w", err)
	}
	return nil
}

// DeleteFolderID drops the cached folder id for (user, cacheKey).
func (r *Repo) DeleteFolderID(ctx context.Context, userID, cacheKey string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.DeleteDriveFolder(ctx, DeleteDriveFolderParams{UserID: uid, Category: cacheKey}); err != nil {
		return fmt.Errorf("delete drive folder: %w", err)
	}
	return nil
}
