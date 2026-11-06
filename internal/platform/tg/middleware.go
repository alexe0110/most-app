package tg

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Users upserts a Telegram user and returns the internal user id. The port is
// declared here, at the consumer; docsbot/postgres implements it.
type Users interface {
	Ensure(ctx context.Context, tgUserID int64, lang string) (string, error)
}

// ctxKey is unexported so no other package can collide with our context values.
type ctxKey int

const userIDKey ctxKey = iota

// UserIDFromContext returns the internal user id stashed by the ensureUser
// middleware, if the update had a sender.
func UserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

// recoverPanic turns a handler panic into a logged error. Handlers run in their
// own goroutine, so the recover must live inside the chain.
func recoverPanic(log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic in telegram handler", "recover", r, "update_id", update.ID)
				}
			}()
			next(ctx, b, update)
		}
	}
}

// logUpdate records that an update arrived. It never touches message text or
// captions, per the project's logging rules.
func logUpdate(log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			attrs := []any{"update_id", update.ID, "kind", updateKind(update)}
			if from := sender(update); from != nil {
				attrs = append(attrs, "tg_user_id", from.ID)
			}
			log.Info("message from telegram", attrs...)
			next(ctx, b, update)
		}
	}
}

// ensureUser upserts the sender and stashes the internal user id in ctx.
// Senderless updates pass through; a storage error drops the update.
func ensureUser(users Users, log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			from := sender(update)
			if from == nil {
				next(ctx, b, update)
				return
			}
			id, err := users.Ensure(ctx, from.ID, from.LanguageCode)
			if err != nil {
				log.Error("ensure user", "err", err, "tg_user_id", from.ID)
				return
			}
			next(context.WithValue(ctx, userIDKey, id), b, update)
		}
	}
}

// sender returns the user behind a message or callback, or nil if there's none.
func sender(update *models.Update) *models.User {
	switch {
	case update.Message != nil:
		return update.Message.From
	case update.CallbackQuery != nil:
		return &update.CallbackQuery.From
	default:
		return nil
	}
}

// updateKind is a coarse label for logs, with no message content.
func updateKind(update *models.Update) string {
	switch {
	case update.Message != nil:
		return "message"
	case update.CallbackQuery != nil:
		return "callback"
	default:
		return "other"
	}
}
