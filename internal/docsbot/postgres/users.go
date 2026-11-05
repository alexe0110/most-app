package postgres

import (
	"context"
	"fmt"
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
