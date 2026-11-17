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
// handler wins. Commands come first so they always work; then a user in the
// feedback mode, whose next message is feedback and nothing else; then the
// team's replies in the feedback chat, so no live /convert session can swallow
// them; then the /convert session claims its user's messages; then document
// callbacks, unsupported media and files; the catch-all text handler (search) is
// last. docs.route repeats the order of the last handlers for a reply that turns
// out to be an ordinary message: keep the two in sync.
func Register(b *bot.Bot, deps Deps, opts ...Option) {
	d := newDocs(deps.Svc, deps.Auth, deps.Log, opts...)
	conv := converter.New(converter.Deps{Log: deps.Log, Svc: deps.Svc, Files: deps.Files, Saved: d, Problems: d})
	d.albumPDFs = conv

	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, d.leavingFeedback(d.start))
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, d.leavingFeedback(d.help))
	b.RegisterHandler(bot.HandlerTypeMessageText, "settings", bot.MatchTypeCommand, d.leavingFeedback(d.settings))
	b.RegisterHandler(bot.HandlerTypeMessageText, "list", bot.MatchTypeCommand, d.leavingFeedback(d.list))
	b.RegisterHandler(bot.HandlerTypeMessageText, "export", bot.MatchTypeCommand, d.leavingFeedback(d.exportCmd)) // not in Commands(): reached from the delete-data prompt
	b.RegisterHandler(bot.HandlerTypeMessageText, "feedback", bot.MatchTypeCommand, d.feedback)
	b.RegisterHandlerMatchFunc(d.ownsFeedback, d.onFeedbackMessage) // before the converter: the feedback prompt wins over a live /convert session
	b.RegisterHandlerMatchFunc(d.ownsFeedbackReply, d.onFeedbackReply)
	conv.Register(b) // /convert, /cancel, its buttons and in-session messages
	b.RegisterHandlerMatchFunc(ownsInline, d.inline)
	b.RegisterHandlerMatchFunc(ownsChosenInline, d.chosenInline)
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
		{Command: "feedback", Description: cmdDescFeedback},
		{Command: "help", Description: cmdDescHelp},
	}
}
