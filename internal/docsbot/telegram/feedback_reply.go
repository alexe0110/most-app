package telegram

import (
	"context"
	"errors"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// ownsFeedbackReply matches a reply in the feedback chat. Only such a reply can be
// an answer to a user; whether it is one depends on the message it answers.
func (d *docs) ownsFeedbackReply(u *models.Update) bool {
	m := u.Message
	return d.feedbackChat != 0 && m != nil && m.Chat.ID == d.feedbackChat && m.ReplyToMessage != nil
}

// onFeedbackReply sends the team's reply to the user who wrote the message it
// answers, and tells the team how it went. A reply to anything else is not ours:
// in a private feedback chat the owner is also an ordinary user of the bot, so the
// message is handled as usual; in a group it is ignored.
func (d *docs) onFeedbackReply(ctx context.Context, b *bot.Bot, u *models.Update) {
	m := u.Message
	tgUser, err := d.svc.FeedbackAuthor(ctx, m.Chat.ID, m.ReplyToMessage.ID)
	if err != nil {
		if !errors.Is(err, docsbot.ErrFeedbackNotFound) {
			d.log.ErrorContext(ctx, "feedback author", "err", err)
		}
		if m.Chat.Type == models.ChatTypePrivate {
			d.route(ctx, b, u)
		}
		return
	}

	out := d.out(b)
	status := textFeedbackDelivered
	if err := d.deliverAnswer(ctx, out, tgUser, m); err != nil {
		status = textFeedbackNotDelivered
		if tg.IsBlocked(err) {
			status = textFeedbackBlocked
		} else {
			d.log.WarnContext(ctx, "deliver feedback answer", "err", err)
		}
	}
	out.Send(ctx, m.Chat.ID, status)
}

// deliverAnswer sends the user the lead-in and a copy of the team's reply. The
// user's private chat has their user id.
func (d *docs) deliverAnswer(ctx context.Context, out tg.Sender, tgUser int64, answer *models.Message) error {
	if _, err := out.TrySend(ctx, tgUser, textFeedbackAnswer); err != nil {
		return err
	}
	_, err := out.TryCopy(ctx, tgUser, answer.Chat.ID, answer.ID)
	return err
}
