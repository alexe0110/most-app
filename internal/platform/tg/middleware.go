package tg

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Users upserts a Telegram user and returns the internal user id. The port is
// declared here, at the consumer; docsbot/postgres implements it.
type Users interface {
	Ensure(ctx context.Context, tgUserID int64, lang string) (string, error)
	// Lookup finds an existing user without creating one. Soft-deleted users
	// are reported as not found.
	Lookup(ctx context.Context, tgUserID int64) (id string, found bool, err error)
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

// ContextWithUserID stores the internal user id in ctx, as the ensureUser
// middleware does. Exposed so an inbound adapter's tests can inject it without
// the middleware.
func ContextWithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// recoverPanic turns a handler panic into a logged error. Handlers run in their
// own goroutine, so the recover must live inside the chain.
func recoverPanic(log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			defer func() {
				if r := recover(); r != nil {
					log.ErrorContext(ctx, "panic in telegram handler", "recover", r, "update_id", update.ID)
				}
			}()
			next(ctx, b, update)
		}
	}
}

// handlerTimeout is the budget of one update handler. It is above the slowest
// regular flow (several Telegram downloads plus a PDF operation) and exists so a
// hung call to Telegram or Drive cannot hold a handler goroutine forever.
const handlerTimeout = 2 * time.Minute

// withTimeout bounds a handler by d. Updates for which unbounded returns true
// (nil means none) get no deadline: a long job that is paced and capped by its
// own logic, like resending all documents, would be cut off mid-way otherwise.
func withTimeout(d time.Duration, unbounded func(*models.Update) bool, log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if unbounded != nil && unbounded(update) {
				next(ctx, b, update)
				return
			}
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			next(ctx, b, update)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				log.WarnContext(ctx, "telegram handler timed out", "update_id", update.ID, "kind", updateKind(update))
			}
		}
	}
}

// leaveFunc tells a non-private chat the bot is private-only and leaves it.
type leaveFunc func(ctx context.Context, b *bot.Bot, chatID int64)

// privateOnly lets only private-chat updates through, so documents never leak
// into a group. In a group or supergroup it calls leave (notice + leave the
// chat); channel posts and updates without a chat are dropped silently. It runs
// before ensureUser, so nothing is stored for them. Inline queries pass: they
// carry no chat and answer only to the sender.
func privateOnly(leave leaveFunc, log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if update.InlineQuery != nil {
				// An inline query has no chat: the user is typing in some other chat.
				next(ctx, b, update)
				return
			}
			chat := chatOf(update)
			switch {
			case chat != nil && chat.Type == models.ChatTypePrivate:
				next(ctx, b, update)
			case chat != nil && (chat.Type == models.ChatTypeGroup || chat.Type == models.ChatTypeSupergroup):
				if botRemoved(update) {
					// Our own leave (or a kick) echoes back as my_chat_member: leaving again would 403.
					log.DebugContext(ctx, "bot removed from group", "update_id", update.ID, "chat_type", chat.Type)
					return
				}
				log.InfoContext(ctx, "update from group chat dropped", "update_id", update.ID, "chat_type", chat.Type)
				leave(ctx, b, chat.ID)
			default:
				log.DebugContext(ctx, "non-private update dropped", "update_id", update.ID, "kind", updateKind(update))
			}
		}
	}
}

// botRemoved reports a my_chat_member update saying the bot left or was kicked.
func botRemoved(update *models.Update) bool {
	m := update.MyChatMember
	if m == nil {
		return false
	}
	switch m.NewChatMember.Type {
	case models.ChatMemberTypeLeft, models.ChatMemberTypeBanned:
		return true
	}
	return false
}

// leaveGroup posts the notice (when set) and leaves the chat. Failures are only
// logged: the update is dropped either way.
func leaveGroup(notice string, log *slog.Logger) leaveFunc {
	return func(ctx context.Context, b *bot.Bot, chatID int64) {
		if notice != "" {
			if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: notice}); err != nil {
				log.WarnContext(ctx, "send group notice", "err", err)
			}
		}
		if _, err := b.LeaveChat(ctx, &bot.LeaveChatParams{ChatID: chatID}); err != nil {
			log.WarnContext(ctx, "leave group chat", "err", err)
		}
	}
}

// chatOf returns the chat an update happened in, or nil if it has none we know.
func chatOf(update *models.Update) *models.Chat {
	switch {
	case update.Message != nil:
		return &update.Message.Chat
	case update.EditedMessage != nil:
		return &update.EditedMessage.Chat
	case update.ChannelPost != nil:
		return &update.ChannelPost.Chat
	case update.EditedChannelPost != nil:
		return &update.EditedChannelPost.Chat
	case update.CallbackQuery != nil:
		m := update.CallbackQuery.Message
		switch {
		case m.Message != nil:
			return &m.Message.Chat
		case m.InaccessibleMessage != nil:
			return &m.InaccessibleMessage.Chat
		}
	case update.MyChatMember != nil:
		return &update.MyChatMember.Chat
	}
	return nil
}

// logUpdate records that an update arrived. It never touches message text or
// captions, per the project's logging rules.
func logUpdate(log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			log.InfoContext(ctx, "message from telegram", "update_id", update.ID, "kind", updateKind(update))
			next(ctx, b, update)
		}
	}
}

// ensureUser upserts the sender and stashes the internal user id in ctx.
// Senderless updates pass through; a storage error drops the update. An inline
// query only looks the user up: a stranger is not created, the handler just
// gets no id.
func ensureUser(users Users, log *slog.Logger) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if q := update.InlineQuery; q != nil {
				if q.From == nil {
					next(ctx, b, update)
					return
				}
				id, found, err := users.Lookup(ctx, q.From.ID)
				if err != nil {
					log.ErrorContext(ctx, "lookup user", "err", err)
					return
				}
				if found {
					ctx = context.WithValue(ctx, userIDKey, id)
				}
				next(ctx, b, update)
				return
			}
			from := sender(update)
			if from == nil {
				next(ctx, b, update)
				return
			}
			id, err := users.Ensure(ctx, from.ID, from.LanguageCode)
			if err != nil {
				log.ErrorContext(ctx, "ensure user", "err", err)
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
	case update.InlineQuery != nil:
		return "inline_query"
	default:
		return "other"
	}
}
