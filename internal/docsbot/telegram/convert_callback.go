package telegram

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// callback handles a button press.
func (c *converter) callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	c.out(b).answer(ctx, update.CallbackQuery.ID)

	chatID, ok := callbackChat(update)
	if !ok {
		return
	}
	data := update.CallbackQuery.Data
	switch {
	case strings.HasPrefix(data, cbDocPrefix):
		// A double tap must not open two menus: the second one is dropped silently.
		if release, ok := c.claimMenu(chatID, callbackMessageID(update)); ok {
			defer release()
			c.onMenu(ctx, b, chatID, docsbot.DocumentID(strings.TrimPrefix(data, cbDocPrefix)))
		}
		return
	case strings.HasPrefix(data, cbQuickPrefix):
		c.onQuick(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbSavePrefix):
		c.onSave(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbRepAsk):
		c.onReplaceAsk(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbRepYes):
		c.onReplace(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbRepNo):
		c.onReplaceCancel(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbAlbumPrefix):
		c.onAlbum(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbSkipPrefix):
		c.onSkip(ctx, b, update, chatID)
		return
	case data == cbMenuCancel:
		if release, ok := c.claimMenu(chatID, callbackMessageID(update)); ok {
			defer release()
			c.deleteCallbackMessage(ctx, b, update, chatID)
		}
		return
	}

	user := update.CallbackQuery.From.ID
	msgID := callbackMessageID(update)
	// Every session button is valid only on the dialogue message, at its own
	// step; anything else is a stale press with no side effects.
	switch data {
	case cbPDF:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepCollectPDF }); !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		c.next(ctx, b, update, chatID, user, textConvertPDFPrompt, pdfKeyboard())
	case cbPDFActions:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepAwaitPDF }); !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		c.next(ctx, b, update, chatID, user, textConvertPDFAwait, cancelKeyboard())
	case cbSessRotate, cbSessKeep, cbSessDrop, cbSessMerge:
		c.onSessionPDFOp(ctx, b, update, chatID, user, strings.TrimPrefix(data, callbackPrefix+"p:"))
	case cbCompress:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseAction}, func(s *session) { s.step = stepAwaitImage }); !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		c.next(ctx, b, update, chatID, user, textConvertCompressPrompt, cancelKeyboard())
	case cbDone:
		sess, ok := c.sessions.check(user, msgID, stepCollectPDF, stepMergePDF)
		if !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		if sess.step == stepMergePDF {
			c.finishMerge(ctx, b, update, user, chatID)
			return
		}
		c.finishPDF(ctx, b, update, user, chatID)
	case cbCancel:
		if _, ok := c.sessions.claim(user, msgID); !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		c.clearButtons(ctx, b, update, chatID)
		c.out(b).send(ctx, chatID, textConvertCancelled)
	case cbLimit1, cbLimit2, cbLimit5:
		sess, ok := c.sessions.advance(user, msgID, []step{stepChooseLimit}, func(s *session) {
			s.maxBytes = limitBytes(data)
			s.step = stepChooseFormat
		})
		if !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		if !sess.inputPNG {
			// A JPEG or WEBP can only become a JPEG: nothing to ask.
			c.finishCompress(ctx, b, update, user, chatID, convert.JPEG)
			return
		}
		c.next(ctx, b, update, chatID, user, textConvertChooseFormat, formatKeyboard())
	case cbLimitCustom:
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseLimit}, func(s *session) { s.step = stepCustomLimit }); !ok {
			c.stale(ctx, b, update, chatID)
			return
		}
		c.next(ctx, b, update, chatID, user, textConvertCustomLimit, nil)
	case cbFmtJPEG:
		c.finishCompress(ctx, b, update, user, chatID, convert.JPEG)
	case cbFmtPNG:
		c.finishCompress(ctx, b, update, user, chatID, convert.PNG)
	default:
		c.stale(ctx, b, update, chatID)
	}
}

// out is the one way this handler talks to Telegram's message API.
func (c *converter) out(b *bot.Bot) sender { return sender{api: b, log: c.log} }

// stale answers a press that no longer applies (an old message, a wrong step,
// an ended session, a double tap) and strips the message's buttons. It does
// nothing else.
func (c *converter) stale(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	c.out(b).send(ctx, chatID, textStale)
	c.clearButtons(ctx, b, update, chatID)
}

// next moves the dialogue on after a button press: the pressed message becomes
// the next step, and stays the session's dialogue message (or its replacement,
// if Telegram refused the edit).
func (c *converter) next(ctx context.Context, b *bot.Bot, update *models.Update, chatID, user int64, text string, kb *models.InlineKeyboardMarkup) {
	unlock := c.dialogue.lock(user)
	defer unlock()
	c.sessions.setMsg(user, c.show(ctx, b, chatID, callbackMessageID(update), text, kb))
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
