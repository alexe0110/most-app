package telegram

import (
	"context"
	"fmt"

	"github.com/alexe0110/most-app/internal/platform/oauth"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// userLookup resolves an internal user id to the Telegram chat to notify.
// Declared here, at the consumer; docsbot/postgres implements it.
type userLookup interface {
	TgUserID(ctx context.Context, userID string) (int64, error)
}

// NotifyConnected builds the OnConnected callback: after Drive is linked, it
// tells the user in Telegram. The bot owns this message, so it lives here.
func NotifyConnected(b *tg.Bot, users userLookup) oauth.OnConnected {
	return func(ctx context.Context, userID string) error {
		chatID, err := users.TgUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("resolve tg user: %w", err)
		}
		return b.Send(ctx, chatID, textDriveConnected)
	}
}
