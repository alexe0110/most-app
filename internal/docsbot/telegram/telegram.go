// Package telegram is the docsbot inbound adapter: it turns Telegram updates
// into Service calls and renders the replies back as messages and buttons.
package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// Authorizer builds the Google consent URL for a user. Declared here, at the
// consumer; platform/oauth.Service implements it. Only the connect button needs
// it, so the domain never sees OAuth.
type Authorizer interface {
	AuthURL(ctx context.Context, userID, bot string) (string, error)
}

// Register wires the docsbot handlers onto b. Order matters: the first matching
// handler wins. Commands come first so they always work; then the /convert
// session claims its user's messages; then document callbacks and files; the
// catch-all text handler (search) is last.
func Register(b *bot.Bot, log *slog.Logger, svc *docsbot.Service, auth Authorizer) {
	conv := newConverter(log)
	d := newDocs(svc, auth, log)

	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, d.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, d.help)
	b.RegisterHandler(bot.HandlerTypeMessageText, "list", bot.MatchTypeCommand, d.list)
	b.RegisterHandler(bot.HandlerTypeMessageText, "convert", bot.MatchTypeCommand, conv.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "cancel", bot.MatchTypeCommand, conv.cancel)
	b.RegisterHandlerMatchFunc(conv.ownsCallback, conv.callback)
	b.RegisterHandlerMatchFunc(conv.ownsMessage, conv.message)
	b.RegisterHandlerMatchFunc(d.ownsCallback, d.callback)
	b.RegisterHandlerMatchFunc(d.ownsFile, d.receive)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, d.onText)
}

// Commands is the bot's command menu, published at startup via setMyCommands
// (same for dev and prod, so BotFather is never edited by hand).
func Commands() []models.BotCommand {
	return []models.BotCommand{
		{Command: "start", Description: "Начать и подключить Google Drive"},
		{Command: "list", Description: "Мои документы по категориям"},
		{Command: "convert", Description: "Фото → PDF или сжать изображение"},
		{Command: "help", Description: "Что умеет бот"},
	}
}

// reply sends text and logs a send failure; a handler can do nothing more.
func reply(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		log.Error("send telegram message", "err", err, "chat_id", chatID)
	}
}
