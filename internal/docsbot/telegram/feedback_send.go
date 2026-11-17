package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// feedbackSendTimeout bounds one request: the limit check, the header, the copies
// and the record. An album flush runs outside the update's deadline.
const feedbackSendTimeout = 30 * time.Second

// feedbackRequest is a message of the user on its way to the team.
type feedbackRequest struct {
	user   docsbot.UserID
	from   *models.User
	chatID int64 // the user's chat
	ids    []int // the user's messages to copy, ascending
	entry  feedbackEntry
}

// sendFeedback copies the user's messages to the feedback chat under a header and
// remembers which messages there belong to the user. What reached the chat is
// recorded even if a later step failed: a reply to it must still find the user.
// Nothing the user wrote is logged.
func (d *docs) sendFeedback(ctx context.Context, b *bot.Bot, req feedbackRequest) {
	ctx, cancel := context.WithTimeout(ctx, feedbackSendTimeout)
	defer cancel()
	out := d.out(b)
	if req.entry.promptMsgID != 0 {
		out.ClearKeyboard(ctx, req.chatID, req.entry.promptMsgID)
	}

	err := d.svc.CheckFeedback(ctx, req.user)
	if errors.Is(err, docsbot.ErrFeedbackLimit) {
		out.Send(ctx, req.chatID, textFeedbackLimit)
		return
	}
	if err == nil {
		var delivered []int
		delivered, err = d.deliverFeedback(ctx, out, req)
		if len(delivered) > 0 {
			// The team has the message either way: a lost link only costs the reply
			// route, so it is logged and does not turn a delivered request into a failure.
			if rerr := d.svc.RecordFeedback(ctx, req.user, d.feedbackChat, delivered); rerr != nil {
				d.log.ErrorContext(ctx, "record feedback", "err", rerr, "user_id", req.user)
			}
		}
	}
	if err != nil {
		d.log.ErrorContext(ctx, "send feedback", "err", err, "user_id", req.user, "source", req.entry.source)
		out.Send(ctx, req.chatID, textFeedbackFailed)
		return
	}
	// The source only, never the text. A request that failed halfway is not "sent".
	d.svc.Track(ctx, req.user, docsbot.EventFeedbackSent, docsbot.EventProps{Source: string(req.entry.source)})
	out.Send(ctx, req.chatID, textFeedbackSent)
}

// deliverFeedback posts the header and the copies to the feedback chat and returns
// the ids of the messages that got there, with the first error.
func (d *docs) deliverFeedback(ctx context.Context, out tg.Sender, req feedbackRequest) ([]int, error) {
	header, err := out.TrySend(ctx, d.feedbackChat, feedbackHeader(req, time.Now()))
	if err != nil {
		return nil, err
	}
	delivered := []int{header}
	if len(req.ids) == 1 {
		id, err := out.TryCopy(ctx, d.feedbackChat, req.chatID, req.ids[0])
		if err != nil {
			return delivered, err
		}
		return append(delivered, id), nil
	}
	copies, err := out.TryCopyMany(ctx, d.feedbackChat, req.chatID, req.ids)
	if err != nil {
		return delivered, err
	}
	return append(delivered, copies...), nil
}
