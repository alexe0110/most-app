package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// defaultLang matches the users.lang column default and is used when Telegram
// reports no language for the sender.
const defaultLang = "ru"

// Ensure upserts a user by their Telegram id and returns the internal user id.
// It implements the platform/tg Users port (declared there, at the consumer).
func (r *Repo) Ensure(ctx context.Context, tgUserID int64, lang string) (string, error) {
	if lang == "" {
		lang = defaultLang
	}
	u, err := r.EnsureUser(ctx, EnsureUserParams{TgUserID: tgUserID, Lang: lang})
	if err != nil {
		return "", fmt.Errorf("ensure user: %w", err)
	}
	return u.ID.String(), nil
}

// Lookup finds an active user by Telegram id without creating one. A missing or
// soft-deleted user is reported as not found. It implements the platform/tg
// Users port.
func (r *Repo) Lookup(ctx context.Context, tgUserID int64) (string, bool, error) {
	u, err := r.GetUserByTgID(ctx, tgUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup user: %w", err)
	}
	return u.ID.String(), true, nil
}

// TgUserID resolves an internal user id (UUID) to its Telegram id, for sending
// the user a message outside an update handler.
func (r *Repo) TgUserID(ctx context.Context, userID string) (int64, error) {
	id, err := parseUUID(userID)
	if err != nil {
		return 0, err
	}
	tgID, err := r.GetUserTgID(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("get tg user id: %w", err)
	}
	return tgID, nil
}
