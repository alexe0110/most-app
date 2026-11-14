package telegram

import (
	"context"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// inlineCacheSeconds is how long Telegram may reuse an answer: short, so a newly
// saved document or a flipped setting shows up almost at once.
const inlineCacheSeconds = 5

// ownsInline matches an inline query (the user typing @bot in some chat).
func ownsInline(u *models.Update) bool { return u.InlineQuery != nil }

// ownsChosenInline matches the report that the user picked one of our results.
func ownsChosenInline(u *models.Update) bool { return u.ChosenInlineResult != nil }

// chosenInline records that a document was sent from inline mode. Telegram sends
// this only after the user picks a result (an inline query fires on every
// keystroke) and only when inline feedback is on in BotFather. A stranger has no
// id in ctx and is not counted. The query text and the result id are not read.
func (d *docs) chosenInline(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	if user, ok := userID(ctx); ok {
		d.svc.Track(ctx, user, docsbot.EventInlineSent, docsbot.EventProps{})
	}
}

// inline answers an inline query with the sender's own documents. The sender may
// be unknown (no user in ctx): they get an empty answer with a button to the bot.
// Query text, titles and file ids are never logged.
func (d *docs) inline(ctx context.Context, b *bot.Bot, u *models.Update) {
	q := u.InlineQuery
	user, _ := userID(ctx) // "" for a stranger
	res, err := d.svc.Inline(ctx, user, q.Query, q.Offset)
	if err != nil {
		d.log.ErrorContext(ctx, "inline query", "err", err, "user_id", user)
		res = docsbot.InlineResult{} // answer with nothing so the client stops waiting
	}
	results, button := renderInline(res)

	start := time.Now()
	_, err = b.AnswerInlineQuery(ctx, &bot.AnswerInlineQueryParams{
		InlineQueryID: q.ID,
		Results:       results,
		CacheTime:     inlineCacheSeconds,
		IsPersonal:    true,
		NextOffset:    res.NextOffset,
		Button:        button,
	})
	tg.TraceAPI(ctx, d.log, "answerInlineQuery", start)
	if err != nil {
		d.log.ErrorContext(ctx, "answer inline query", "err", err, "user_id", user)
		return
	}
	d.log.InfoContext(ctx, "inline answered", "user_id", user, "count", len(results), "state", int(res.State))
}
