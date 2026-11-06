// Package telegram is the docsbot inbound adapter: it registers the bot's
// handlers on the shared Telegram client and renders replies. For now it only
// greets on /start and falls back on anything else; the real use cases arrive
// with the domain Service in #9/#10.
package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Register wires the docsbot handlers onto b. Order matters: handlers are tried
// in registration order and the first match wins. Commands come first so /start,
// /convert and /cancel always work; then the converter claims the updates of a
// user mid-conversion; a catch-all text handler, registered last, replies to
// anything else.
func Register(b *bot.Bot, log *slog.Logger) {
	conv := newConverter(log)

	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler(log))
	b.RegisterHandler(bot.HandlerTypeMessageText, "convert", bot.MatchTypeCommand, conv.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "cancel", bot.MatchTypeCommand, conv.cancel)
	b.RegisterHandlerMatchFunc(conv.ownsCallback, conv.callback)
	b.RegisterHandlerMatchFunc(conv.ownsMessage, conv.message)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, unknownHandler(log))
}

func startHandler(log *slog.Logger) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		reply(ctx, b, log, update.Message.Chat.ID, startText)
	}
}

func unknownHandler(log *slog.Logger) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		reply(ctx, b, log, update.Message.Chat.ID, unknownText)
	}
}

// reply sends text to chat and logs a send failure once; a handler has nothing
// better to do with the error.
func reply(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		log.Error("send telegram message", "err", err, "chat_id", chatID)
	}
}
