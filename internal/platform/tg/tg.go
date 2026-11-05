// Package tg is the shared Telegram entry point for every bot in the series:
// it builds the underlying client, wires the common middleware, serves the
// webhook and runs either long polling or webhook mode driven by ctx.
package tg

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Mode selects how the bot receives updates from Telegram.
type Mode string

// Receive modes: long polling locally, webhook in production.
const (
	ModePolling Mode = "polling"
	ModeWebhook Mode = "webhook"
)

// webhookPath is where the webhook handler lives on the shared HTTP mux.
const webhookPath = "/tg/webhook"

// Config carries the Telegram settings resolved from the environment.
type Config struct {
	Token         string
	Mode          Mode
	PublicURL     string // base URL for the webhook, webhook mode only
	WebhookSecret string // shared secret proving a request came from Telegram
}

// Bot wraps the go-telegram client with the series' lifecycle. The SDK client
// is created here with the common middleware; the inbound adapter registers its
// handlers through the register callback and never sees the client otherwise.
type Bot struct {
	api *bot.Bot
	cfg Config
}

// New builds the bot, installs the common middleware (panic recovery, update
// logging and user upsert) and lets the inbound adapter add its handlers via
// register.
func New(cfg Config, users Users, log *slog.Logger, register func(*bot.Bot)) (*Bot, error) {
	opts := []bot.Option{
		bot.WithMiddlewares(recoverPanic(log), logUpdate(log), ensureUser(users, log)),
		// Updates that no handler matched are already logged by the middleware,
		// so the default handler stays silent instead of writing to stdlib log.
		bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}),
	}
	if cfg.Mode == ModeWebhook {
		opts = append(opts, bot.WithWebhookSecretToken(cfg.WebhookSecret))
	}

	api, err := bot.New(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("new telegram bot: %w", err)
	}
	register(api)

	return &Bot{api: api, cfg: cfg}, nil
}

// RegisterRoutes mounts the webhook handler on mux. It is a no-op in polling
// mode, so callers can always call it.
func (b *Bot) RegisterRoutes(mux *http.ServeMux) {
	if b.cfg.Mode != ModeWebhook {
		return
	}
	mux.HandleFunc("POST "+webhookPath, webhookHandler(b.cfg.WebhookSecret, b.api.WebhookHandler()))
}

// Run starts receiving updates and blocks until ctx is cancelled. In webhook
// mode it registers the webhook with Telegram first; a failure there is
// returned before anything starts.
func (b *Bot) Run(ctx context.Context) error {
	if b.cfg.Mode == ModeWebhook {
		if _, err := b.api.SetWebhook(ctx, &bot.SetWebhookParams{
			URL:         strings.TrimRight(b.cfg.PublicURL, "/") + webhookPath,
			SecretToken: b.cfg.WebhookSecret,
		}); err != nil {
			return fmt.Errorf("set webhook: %w", err)
		}
		b.api.StartWebhook(ctx)
		return nil
	}

	b.api.Start(ctx)
	return nil
}

// Send delivers a plain-text message to a chat. It is for notifications that
// originate outside an update handler, such as the OAuth callback.
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// webhookHandler guards the SDK webhook handler with the secret-token check.
// The SDK's own handler only logs a bad secret and still returns 200, so we
// enforce 401 ourselves before delegating. A missing secret config means the
// header must also be empty — but the bot is never run in webhook mode without
// a secret (config enforces it).
func webhookHandler(secret string, inner http.HandlerFunc) http.HandlerFunc {
	want := []byte(secret)
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner(w, r)
	}
}
