package telegram

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/oauth"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// userLookup resolves an internal user id to the Telegram chat to notify.
// Declared here, at the consumer; docsbot/postgres implements it.
type userLookup interface {
	TgUserID(ctx context.Context, userID string) (int64, error)
}

// driveCounter reports how many saved documents are not yet on Drive.
type driveCounter interface {
	NotBackedUpCount(ctx context.Context, user docsbot.UserID) (int, error)
}

// NotifyConnected builds the OnConnected callback: after Drive is linked it tells
// the user, and offers to transfer any documents still only in Telegram.
func NotifyConnected(b *tg.Bot, users userLookup, counter driveCounter) oauth.OnConnected {
	return func(ctx context.Context, userID string) error {
		chatID, err := users.TgUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("resolve tg user: %w", err)
		}
		if err := b.Send(ctx, chatID, textDriveConnected); err != nil {
			return err
		}
		n, err := counter.NotBackedUpCount(ctx, docsbot.UserID(userID))
		if err != nil {
			return fmt.Errorf("count not backed up: %w", err)
		}
		if n == 0 {
			return nil
		}
		return b.SendMarkup(ctx, chatID, fmt.Sprintf(textTransferOffer, n), transferOfferKeyboard())
	}
}

// Notifier posts upload outcomes to the user in Telegram. It satisfies
// docsbot/jobs.Notifier (checked where it is bound to the worker in cmd).
type Notifier struct {
	bot   *tg.Bot
	users userLookup
	auth  Authorizer
	log   *slog.Logger
}

// NewNotifier builds the Telegram Notifier that the upload worker reports to.
func NewNotifier(b *tg.Bot, users userLookup, auth Authorizer, log *slog.Logger) *Notifier {
	return &Notifier{bot: b, users: users, auth: auth, log: log}
}

// BackedUp tells the user a document reached Drive, with a link to open it.
func (n *Notifier) BackedUp(ctx context.Context, user docsbot.UserID, doc docsbot.Document) error {
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	return n.bot.SendMarkup(ctx, chatID, fmt.Sprintf(textBackedUp, doc.Title), openDriveKeyboard(doc))
}

// BackupFailed tells the user an upload failed for good, with a retry button.
func (n *Notifier) BackupFailed(ctx context.Context, user docsbot.UserID, doc docsbot.Document) error {
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	return n.bot.SendMarkup(ctx, chatID, textBackupFailed, retryKeyboard(doc.ID))
}

// Reconnect asks the user to re-link Drive after access was revoked.
func (n *Notifier) Reconnect(ctx context.Context, user docsbot.UserID) error {
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	url, uErr := n.auth.AuthURL(ctx, string(user), botName)
	if uErr != nil {
		n.log.Error("build auth url", "err", uErr)
		return n.bot.Send(ctx, chatID, textReconnect)
	}
	return n.bot.SendMarkup(ctx, chatID, textReconnect, reconnectKeyboard(url))
}
