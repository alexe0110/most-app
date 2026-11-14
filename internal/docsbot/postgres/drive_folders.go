package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
)

// FolderCache stores the Drive folder ids found or created per user and cache
// key (the category slug, or another key the Drive adapter picks); it implements
// gdrive.FolderCache and the domain's Folders port.
type FolderCache struct{ q *Queries }

var (
	_ gdrive.FolderCache = (*FolderCache)(nil)
	_ docsbot.Folders    = (*FolderCache)(nil)
)

// NewFolderCache wires FolderCache around an existing pool.
func NewFolderCache(pool *pgxpool.Pool) *FolderCache {
	return &FolderCache{q: New(pool)}
}

// FolderID returns the cached Drive folder id for (user, cacheKey).
func (r *FolderCache) FolderID(ctx context.Context, userID, cacheKey string) (string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return "", false, err
	}
	row, err := r.q.GetDriveFolder(ctx, GetDriveFolderParams{UserID: uid, Category: cacheKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get drive folder: %w", err)
	}
	return row.DriveFolderID, true, nil
}

// SaveFolderID stores (or replaces) the Drive folder id for (user, cacheKey).
func (r *FolderCache) SaveFolderID(ctx context.Context, userID, cacheKey, id string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if _, err := r.q.UpsertDriveFolder(ctx, UpsertDriveFolderParams{UserID: uid, Category: cacheKey, DriveFolderID: id}); err != nil {
		return fmt.Errorf("upsert drive folder: %w", err)
	}
	return nil
}

// DeleteAllFolders clears the user's cached Drive folder ids.
func (r *FolderCache) DeleteAllFolders(ctx context.Context, user docsbot.UserID) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	if err := r.q.DeleteUserDriveFolders(ctx, uid); err != nil {
		return fmt.Errorf("delete user drive folders: %w", err)
	}
	return nil
}

// DeleteFolderID drops the cached folder id for (user, cacheKey).
func (r *FolderCache) DeleteFolderID(ctx context.Context, userID, cacheKey string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return err
	}
	if err := r.q.DeleteDriveFolder(ctx, DeleteDriveFolderParams{UserID: uid, Category: cacheKey}); err != nil {
		return fmt.Errorf("delete drive folder: %w", err)
	}
	return nil
}

// CategoryFolderID returns the cached Drive folder id of the user's category.
func (r *FolderCache) CategoryFolderID(ctx context.Context, user docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	return r.FolderID(ctx, string(user), string(cat))
}
