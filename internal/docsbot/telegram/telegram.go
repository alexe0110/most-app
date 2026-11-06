// Package telegram is the docsbot inbound adapter: it registers the bot's
// handlers on the shared Telegram client and renders replies.
package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Register wires the docsbot handlers onto b. Order matters: the first matching
// handler wins. Commands come first so they always work; then the converter
// claims an in-session user's messages; the catch-all text handler is last.
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

// reply sends text and logs a send failure; a handler can do nothing more.
func reply(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		log.Error("send telegram message", "err", err, "chat_id", chatID)
	}
}
