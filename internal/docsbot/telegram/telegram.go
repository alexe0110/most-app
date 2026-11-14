// Package telegram is the docsbot inbound adapter: it turns Telegram updates
// into Service calls and renders the replies back as messages and buttons.
package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
)

// Authorizer builds the Google consent URL for a user. Declared here, at the
// consumer; platform/oauth.Service implements it. Only the connect button needs
// it, so the domain never sees OAuth.
type Authorizer interface {
	AuthURL(ctx context.Context, userID, bot string) (string, error)
}

// Deps are the required collaborators of the adapter.
type Deps struct {
	Log   *slog.Logger
	Svc   *docsbot.Service
	Auth  Authorizer
	Files converter.FileDownloader
}

// Register wires the docsbot handlers onto b. Order matters: the first matching
// handler wins. Commands come first so they always work; then the /convert
// session claims its user's messages; then document callbacks, unsupported media and files; the
// catch-all text handler (search) is last.
func Register(b *bot.Bot, deps Deps, opts ...Option) {
	d := newDocs(deps.Svc, deps.Auth, deps.Log, opts...)
	conv := converter.New(converter.Deps{Log: deps.Log, Svc: deps.Svc, Files: deps.Files, Saved: d})
	d.albumPDFs = conv

	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, d.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, d.help)
	b.RegisterHandler(bot.HandlerTypeMessageText, "settings", bot.MatchTypeCommand, d.settings)
	b.RegisterHandler(bot.HandlerTypeMessageText, "list", bot.MatchTypeCommand, d.list)
	b.RegisterHandler(bot.HandlerTypeMessageText, "export", bot.MatchTypeCommand, d.exportCmd) // not in Commands(): reached from the delete-data prompt
	conv.Register(b)                                                                           // /convert, /cancel, its buttons and in-session messages
	b.RegisterHandlerMatchFunc(ownsInline, d.inline)
	b.RegisterHandlerMatchFunc(d.ownsCallback, d.callback)
	b.RegisterHandlerMatchFunc(d.ownsUnsupported, d.onUnsupported) // before files: an animation also carries a document
	b.RegisterHandlerMatchFunc(d.ownsFile, d.receive)
	b.RegisterHandler(bot.HandlerTypeMessageText, "", bot.MatchTypePrefix, d.onText)
}

// GroupNotice is the text sent to a group before the bot leaves it.
func GroupNotice() string { return textGroupNotice }

// Commands is the bot's command menu, published at startup via setMyCommands
// (same for dev and prod, so BotFather is never edited by hand).
func Commands() []models.BotCommand {
	return []models.BotCommand{
		{Command: "start", Description: cmdDescStart},
		{Command: "list", Description: cmdDescList},
		{Command: "convert", Description: cmdDescConvert},
		{Command: "settings", Description: cmdDescSettings},
		{Command: "help", Description: cmdDescHelp},
	}
}
