package telegram

import (
	"context"
	"errors"
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

// driveCounter is the part of docsbot.Service the connect callback needs: it
// resets the user's cached Drive folders and counts documents not yet on Drive.
type driveCounter interface {
	ResetDriveFolders(ctx context.Context, user docsbot.UserID) error
	NotBackedUpCount(ctx context.Context, user docsbot.UserID) (int, error)
}

// settingsReader is the part of docsbot.Service that redraws the /settings
// screen once Drive is connected.
type settingsReader interface {
	Settings(ctx context.Context, user docsbot.UserID) (docsbot.SettingsView, error)
}

// connectSettings is what NotifyConnected needs to turn the user's /settings
// message into the connected screen.
type connectSettings struct {
	msgs *SettingsMessages
	svc  settingsReader
	auth Authorizer
}

// ConnectOption tunes NotifyConnected.
type ConnectOption func(*connectSettings)

// WithConnectSettings makes NotifyConnected also redraw the /settings message the
// user came from (remembered in msgs by the docs handlers) as the connected screen.
func WithConnectSettings(msgs *SettingsMessages, svc settingsReader, auth Authorizer) ConnectOption {
	return func(c *connectSettings) { c.msgs, c.svc, c.auth = msgs, svc, auth }
}

// NotifyConnected builds the OnConnected callback: after Drive is linked it tells
// the user, refreshes their /settings message when it is known, and offers to
// transfer any documents still only in Telegram.
func NotifyConnected(b *tg.Bot, users userLookup, counter driveCounter, log *slog.Logger, opts ...ConnectOption) oauth.OnConnected {
	var cs connectSettings
	for _, opt := range opts {
		opt(&cs)
	}
	return func(ctx context.Context, userID string) error {
		chatID, err := users.TgUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("resolve tg user: %w", err)
		}
		if err := counter.ResetDriveFolders(ctx, docsbot.UserID(userID)); err != nil {
			return fmt.Errorf("reset drive folders: %w", err)
		}
		log.InfoContext(ctx, "drive folder cache reset", "user_id", userID, "reason", "reconnect")
		if err := b.Send(ctx, chatID, textDriveConnected); err != nil {
			return ignoreBlocked(err) // a block is already logged at Warn by tg.Bot
		}
		log.InfoContext(ctx, "drive connected", "user_id", userID)
		cs.refresh(ctx, b, log, chatID, docsbot.UserID(userID))
		n, err := counter.NotBackedUpCount(ctx, docsbot.UserID(userID))
		if err != nil {
			return fmt.Errorf("count not backed up: %w", err)
		}
		if n == 0 {
			return nil
		}
		return ignoreBlocked(b.SendMarkup(ctx, chatID, fmt.Sprintf(textTransferOffer, n), transferOfferKeyboard()))
	}
}

// refresh edits the remembered /settings message into the connected screen. An
// unknown message (restart, expired) is skipped silently; a failure is logged
// at Warn and never fails the OAuth callback.
func (c connectSettings) refresh(ctx context.Context, b *tg.Bot, log *slog.Logger, chatID int64, user docsbot.UserID) {
	if c.msgs == nil || c.svc == nil {
		return
	}
	msgID, ok := c.msgs.store.get(chatID)
	if !ok {
		return
	}
	view, err := c.svc.Settings(ctx, user)
	if err != nil {
		log.WarnContext(ctx, "load settings after connect", "err", err, "user_id", user)
		return
	}
	var (
		url     string
		haveURL bool
	)
	if c.auth != nil {
		if u, err := c.auth.AuthURL(ctx, string(user), docsbot.BotName); err != nil {
			log.WarnContext(ctx, "build auth url after connect", "err", err, "user_id", user)
		} else {
			url, haveURL = u, true
		}
	}
	text, kb := renderSettings(view, url, haveURL)
	if err := b.EditHTML(ctx, chatID, msgID, text, kb); err != nil && !isNotModified(err) {
		log.WarnContext(ctx, "edit settings after connect", "err", err, "user_id", user)
	}
}

// ignoreBlocked drops tg.ErrBlocked: it is already logged at Warn by tg.Bot and
// is not an error of the caller.
func ignoreBlocked(err error) error {
	if errors.Is(err, tg.ErrBlocked) {
		return nil
	}
	return err
}
