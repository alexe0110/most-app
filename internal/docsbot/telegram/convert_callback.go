package telegram

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// callback handles a button press.
func (c *converter) callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	p := newPress(ctx, update.CallbackQuery)
	c.out(b).answer(ctx, p.queryID)
	if !p.hasMsg {
		return
	}
	switch p.data.action {
	case convActDoc:
		// A double tap must not open two menus: the second one is dropped silently.
		if release, ok := c.claimMenu(p.chatID, p.msgID); ok {
			defer release()
			c.onMenu(ctx, b, p.chatID, p.data.docID)
		}
		return
	case convActQuick:
		c.onQuick(ctx, b, p)
		return
	case convActSave:
		c.onSave(ctx, b, p)
		return
	case convActRepAsk:
		c.onReplaceAsk(ctx, b, p)
		return
	case convActRepYes:
		c.onReplace(ctx, b, p)
		return
	case convActRepNo:
		c.onReplaceCancel(ctx, b, p)
		return
	case convActAlbum:
		c.onAlbum(ctx, b, p)
		return
	case convActSkip:
		c.onSkip(ctx, b, p)
		return
	case convActMenuCancel:
		if release, ok := c.claimMenu(p.chatID, p.msgID); ok {
			defer release()
			c.deleteCallbackMessage(ctx, b, p)
		}
		return
	}
	c.onSessionButton(ctx, b, p)
}

// onSessionButton handles the buttons of a /convert session. Every one is valid
// only on the dialogue message, at its own step; anything else is a stale press
// with no side effects.
func (c *converter) onSessionButton(ctx context.Context, b *bot.Bot, p press) {
	user, msgID := p.tgUser, p.msgID
	switch p.data.action {
	case convActPDF:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepCollectPDF }); !ok {
			c.stale(ctx, b, p)
			return
		}
		c.next(ctx, b, p, textConvertPDFPrompt, pdfKeyboard())
	case convActPDFOps:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepAwaitPDF }); !ok {
			c.stale(ctx, b, p)
			return
		}
		c.next(ctx, b, p, textConvertPDFAwait, cancelKeyboard())
	case convActPDFOp:
		if op := p.data.token(); op == opRotate || op == opKeep || op == opDrop || op == opMerge {
			c.onSessionPDFOp(ctx, b, p, op)
			return
		}
		c.stale(ctx, b, p)
	case convActCompress:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepAwaitImage }); !ok {
			c.stale(ctx, b, p)
			return
		}
		c.next(ctx, b, p, textConvertCompressPrompt, cancelKeyboard())
	case convActDone:
		sess, ok := c.sessions.check(user, msgID, stepCollectPDF, stepMergePDF)
		if !ok {
			c.stale(ctx, b, p)
			return
		}
		if sess.step == stepMergePDF {
			c.finishMerge(ctx, b, p)
			return
		}
		c.finishPDF(ctx, b, p)
	case convActCancel:
		if _, ok := c.sessions.claim(user, msgID); !ok {
			c.stale(ctx, b, p)
			return
		}
		c.clearButtons(ctx, b, p)
		c.out(b).send(ctx, p.chatID, textConvertCancelled)
	case convActLimit1, convActLimit2, convActLimit5:
		sess, ok := c.sessions.advance(user, msgID, []step{stepChooseLimit}, func(s *session) {
			s.maxBytes = limitBytes(p.data.action)
			s.step = stepChooseFormat
		})
		if !ok {
			c.stale(ctx, b, p)
			return
		}
		if !sess.inputPNG {
			// A JPEG or WEBP can only become a JPEG: nothing to ask.
			c.finishCompress(ctx, b, p, convert.JPEG)
			return
		}
		c.next(ctx, b, p, textConvertChooseFormat, formatKeyboard())
	case convActLimitCustom:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseLimit}, func(s *session) { s.step = stepCustomLimit }); !ok {
			c.stale(ctx, b, p)
			return
		}
		c.next(ctx, b, p, textConvertCustomLimit, nil)
	case convActFmtJPEG:
		c.finishCompress(ctx, b, p, convert.JPEG)
	case convActFmtPNG:
		c.finishCompress(ctx, b, p, convert.PNG)
	default:
		c.stale(ctx, b, p)
	}
}

// out is the one way this handler talks to Telegram's message API.
func (c *converter) out(b *bot.Bot) sender { return sender{api: b, log: c.log} }

// stale answers a press that no longer applies (an old message, a wrong step,
// an ended session, a double tap) and strips the message's buttons. It does
// nothing else.
func (c *converter) stale(ctx context.Context, b *bot.Bot, p press) {
	c.out(b).send(ctx, p.chatID, textStale)
	c.clearButtons(ctx, b, p)
}

// next moves the dialogue on after a button press: the pressed message becomes
// the next step, and stays the session's dialogue message (or its replacement,
// if Telegram refused the edit).
func (c *converter) next(ctx context.Context, b *bot.Bot, p press, text string, kb *models.InlineKeyboardMarkup) {
	unlock := c.dialogue.lock(p.tgUser)
	defer unlock()
	c.sessions.setMsg(p.tgUser, c.show(ctx, b, p.chatID, p.msgID, text, kb))
}

// show puts text and the keyboard (nil for none) on message msgID by editing it.
// If the edit fails (the message is gone, too old, can't be edited) it sends a new
// message instead; either way it returns the id of the live message. An edit that
// changes nothing counts as success.
func (c *converter) show(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) int {
	out := c.out(b)
	if msgID > 0 {
		// An omitted keyboard keeps the old one, which is what a nil kb means here.
		var opts []msgOpt
		if kb != nil {
			opts = append(opts, withKeyboard(kb))
		}
		err := out.tryEdit(ctx, chatID, msgID, text, opts...)
		if err == nil {
			return msgID
		}
		out.logFailure(ctx, slog.LevelWarn, "edit dialogue message failed, sending a new one", err)
	}
	if id := out.send(ctx, chatID, text, withKeyboard(kb)); id != 0 {
		return id
	}
	return msgID // both failed: keep the old dialogue message so the session survives
}

// moveDown shows the next step of a dialogue that a user's message advanced: a
// new message at the bottom of the chat, next to what the user just sent, then
// the previous dialogue message is removed. Callers hold the user's dialogue lock.
func (c *converter) moveDown(ctx context.Context, b *bot.Bot, chatID, user int64, text string, kb *models.InlineKeyboardMarkup) {
	prev := 0
	if sess, ok := c.sessions.live(user); ok {
		prev = sess.msgID
	}
	id := c.out(b).send(ctx, chatID, text, withKeyboard(kb))
	if id == 0 {
		return // keep the previous dialogue message: the session still owns it
	}
	c.sessions.setMsg(user, id)
	if prev != 0 && prev != id {
		c.drop(ctx, b, chatID, prev)
	}
}

// drop deletes a dialogue message; if Telegram refuses (too old), its buttons are
// removed instead so they can't be pressed.
func (c *converter) drop(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	out := c.out(b)
	if err := out.tryDelete(ctx, chatID, msgID); err != nil {
		out.clearKeyboard(ctx, chatID, msgID)
	}
}

// finish removes the progress message once the result is sent; if it can't be
// deleted it turns into a short "done" note without buttons.
func (c *converter) finish(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	if err := c.out(b).tryDelete(ctx, chatID, msgID); err != nil {
		c.show(ctx, b, chatID, msgID, textConvertDone, nil)
	}
}

// retry shows an error on the dialogue message with a keyboard and puts the
// claimed session back, so the user can try again.
// Callers must not hold the user's dialogue lock.
func (c *converter) retry(ctx context.Context, b *bot.Bot, chatID, user int64, sess session, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	unlock := c.dialogue.lock(user)
	defer unlock()
	sess.msgID = c.show(ctx, b, chatID, msgID, text, kb)
	c.sessions.restore(user, sess)
}

// claimMenu marks a menu message as being worked on; false means another tap on
// it is already running.
func (c *converter) claimMenu(chatID int64, msgID int) (release func(), ok bool) {
	key := strconv.FormatInt(chatID, 10) + ":" + strconv.Itoa(msgID)
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	if _, busy := c.inflight[key]; busy {
		return nil, false
	}
	c.inflight[key] = struct{}{}
	return func() {
		c.inflightMu.Lock()
		defer c.inflightMu.Unlock()
		delete(c.inflight, key)
	}, true
}
