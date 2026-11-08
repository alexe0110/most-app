// Package tg is the shared Telegram entry point for every bot: it builds the
// client, wires the common middleware, and runs polling or webhook mode.
package tg

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

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

const (
	maxFileBytes = 20 << 20 // Bot API download limit
	downloadWait = 30 * time.Second
)

// ErrFileTooLarge means the file is above the Bot API download limit (20 MB).
var ErrFileTooLarge = errors.New("tg: file exceeds download limit")

// Config carries the Telegram settings resolved from the environment.
type Config struct {
	Token         string
	Mode          Mode
	PublicURL     string // base URL for the webhook, webhook mode only
	WebhookSecret string // shared secret proving a request came from Telegram
}

// Bot wraps the go-telegram client. The inbound adapter adds its handlers
// through the register callback and never sees the client otherwise.
type Bot struct {
	api  *bot.Bot
	cfg  Config
	http *http.Client
	log  *slog.Logger
}

// New builds the bot, installs the common middleware (panic recovery, update
// logging, user upsert) and lets the adapter add handlers via register.
func New(cfg Config, users Users, log *slog.Logger, register func(*bot.Bot)) (*Bot, error) {
	opts := []bot.Option{
		bot.WithMiddlewares(recoverPanic(log), logUpdate(log), ensureUser(users, log)),
		// Unmatched updates are already logged by the middleware; stay silent.
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

	return &Bot{api: api, cfg: cfg, http: &http.Client{Timeout: downloadWait}, log: log}, nil
}

// RegisterRoutes mounts the webhook handler on mux. It is a no-op in polling
// mode, so callers can always call it.
func (b *Bot) RegisterRoutes(mux *http.ServeMux) {
	if b.cfg.Mode != ModeWebhook {
		return
	}
	mux.HandleFunc("POST "+webhookPath, webhookHandler(b.cfg.WebhookSecret, b.api.WebhookHandler()))
}

// Run starts receiving updates until ctx is cancelled. In webhook mode it
// registers the webhook with Telegram first.
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

// Send delivers a plain-text message, for notifications sent outside an update
// handler (e.g. the OAuth callback).
func (b *Bot) Send(ctx context.Context, chatID int64, text string) error {
	start := time.Now()
	_, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text})
	b.trace(ctx, "sendMessage", start, err)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// SendMarkup delivers a message with a reply markup (e.g. an inline keyboard),
// for notifications sent outside an update handler.
func (b *Bot) SendMarkup(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) error {
	start := time.Now()
	_, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup})
	b.trace(ctx, "sendMessage", start, err)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return nil
}

// SetCommands publishes the bot's command menu (setMyCommands). Called once at
// startup so the menu is defined in code, the same for dev and prod.
func (b *Bot) SetCommands(ctx context.Context, commands []models.BotCommand) error {
	if len(commands) == 0 {
		return nil
	}
	start := time.Now()
	_, err := b.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: commands})
	b.trace(ctx, "setMyCommands", start, err)
	if err != nil {
		return fmt.Errorf("set my commands: %w", err)
	}
	return nil
}

// Download fetches a Telegram file's bytes and content type by file_id, capped
// at the Bot API's 20 MB download limit; a larger file returns ErrFileTooLarge.
func (b *Bot) Download(ctx context.Context, fileID string) (data []byte, mime string, err error) {
	start := time.Now()
	defer func() { b.trace(ctx, "getFile+download", start, err) }()

	ctx, cancel := context.WithTimeout(ctx, downloadWait)
	defer cancel()

	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too big") {
			return nil, "", ErrFileTooLarge
		}
		return nil, "", fmt.Errorf("get file: %w", err)
	}
	if file.FileSize > maxFileBytes {
		return nil, "", ErrFileTooLarge
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api.FileDownloadLink(file), nil)
	if err != nil {
		return nil, "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		// The download URL embeds the bot token; redact it before it can be logged.
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = strings.ReplaceAll(ue.URL, b.cfg.Token, "***")
		}
		return nil, "", fmt.Errorf("download file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download file: status %d", resp.StatusCode)
	}

	data, err = io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxFileBytes {
		return nil, "", ErrFileTooLarge
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// trace logs one Telegram API call at Debug: the method and timing, never
// the payload, URL or token.
func (b *Bot) trace(ctx context.Context, op string, start time.Time, err error) {
	attrs := []any{"op", op, "duration_ms", time.Since(start).Milliseconds()}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	b.log.DebugContext(ctx, "telegram api", attrs...)
}

// webhookHandler adds the secret-token check: the SDK only logs a bad secret
// and still returns 200, so we enforce 401 before delegating. Config guarantees
// the secret is non-empty in webhook mode.
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
