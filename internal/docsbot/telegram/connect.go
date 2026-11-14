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

// driveCounter is the part of docsbot.Service the connect callback needs: it
// records the connect (resetting the user's cached Drive folders) and counts
// documents not yet on Drive.
type driveCounter interface {
	OnDriveConnected(ctx context.Context, user docsbot.UserID) error
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
	info screenInfo
}

// ConnectOption tunes NotifyConnected.
type ConnectOption func(*connectSettings)

// WithConnectSettings makes NotifyConnected also redraw the /settings message the
// user came from (remembered in msgs by the docs handlers) as the connected screen.
func WithConnectSettings(msgs *SettingsMessages, svc settingsReader, auth Authorizer) ConnectOption {
	return func(c *connectSettings) { c.msgs, c.svc, c.auth = msgs, svc, auth }
}

// WithConnectBot sets the bot's @username and the privacy page URL named on the
// redrawn settings screen; an empty value leaves its line out.
func WithConnectBot(username, privacyURL string) ConnectOption {
	return func(c *connectSettings) { c.info.botUsername, c.info.privacyURL = username, privacyURL }
}

// NotifyConnected builds the OnConnected callback: after Drive is linked it tells
// the user, refreshes their /settings message when it is known, and offers to
// transfer any documents still only in Telegram.
func NotifyConnected(b *tg.Bot, users userLookup, counter driveCounter, log *slog.Logger, opts ...ConnectOption) oauth.OnConnected {
	var cs connectSettings
	for _, opt := range opts {
		opt(&cs)
	}
	out := tg.NewSender(b.Client(), log)
	return func(ctx context.Context, userID string) error {
		chatID, err := users.TgUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("resolve tg user: %w", err)
		}
		if err := counter.OnDriveConnected(ctx, docsbot.UserID(userID)); err != nil {
			return fmt.Errorf("reset drive folders: %w", err)
		}
		log.InfoContext(ctx, "drive folder cache reset", "user_id", userID, "reason", "reconnect")
		if _, err := out.TrySend(ctx, chatID, textDriveConnected); err != nil {
			return out.IgnoreBlocked(ctx, err)
		}
		log.InfoContext(ctx, "drive connected", "user_id", userID)
		cs.refresh(ctx, out, chatID, docsbot.UserID(userID))
		n, err := counter.NotBackedUpCount(ctx, docsbot.UserID(userID))
		if err != nil {
			return fmt.Errorf("count not backed up: %w", err)
		}
		if n == 0 {
			return nil
		}
		_, err = out.TrySend(ctx, chatID, fmt.Sprintf(textTransferOffer, n, plural(n, pluralDocumentOne, pluralDocumentFew, pluralDocumentMany)), tg.WithKeyboard(transferOfferKeyboard()))
		return out.IgnoreBlocked(ctx, err)
	}
}

// refresh edits the remembered /settings message into the connected screen. An
// unknown message (restart, expired) is skipped silently; a failure is logged
// at Warn and never fails the OAuth callback.
func (c connectSettings) refresh(ctx context.Context, out tg.Sender, chatID int64, user docsbot.UserID) {
	if c.msgs == nil || c.svc == nil {
		return
	}
	msgID, ok := c.msgs.m.Get(chatID)
	if !ok {
		return
	}
	view, err := c.svc.Settings(ctx, user)
	if err != nil {
		out.LogFailure(ctx, slog.LevelWarn, "load settings after connect", err, "user_id", user)
		return
	}
	var (
		url     string
		haveURL bool
	)
	if c.auth != nil {
		if u, err := c.auth.AuthURL(ctx, string(user), docsbot.BotName); err != nil {
			out.LogFailure(ctx, slog.LevelWarn, "build auth url after connect", err, "user_id", user)
		} else {
			url, haveURL = u, true
		}
	}
	text, kb := renderSettings(view, url, haveURL, c.info)
	err = out.TryEdit(ctx, chatID, msgID, text, tg.WithHTML(), tg.WithKeyboard(kb))
	out.LogFailure(ctx, slog.LevelWarn, "edit settings after connect", err, "user_id", user)
}
