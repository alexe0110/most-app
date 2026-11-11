package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// callbackPrefix tags every inline button this feature owns.
const callbackPrefix = "conv:"

// Callback data for the inline buttons.
const (
	cbPDF         = callbackPrefix + "pdf"
	cbCompress    = callbackPrefix + "compress"
	cbDone        = callbackPrefix + "done"
	cbCancel      = callbackPrefix + "cancel"
	cbLimit1      = callbackPrefix + "limit1"
	cbLimit2      = callbackPrefix + "limit2"
	cbLimit5      = callbackPrefix + "limit5"
	cbLimitCustom = callbackPrefix + "limitcustom"
	cbFmtJPEG     = callbackPrefix + "fmtjpeg"
	cbFmtPNG      = callbackPrefix + "fmtpng"
	cbMenuCancel  = callbackPrefix + "x" // closes the convert menu
	cbPDFActions  = callbackPrefix + "pdfops"

	// PDF operations on a PDF sent to /convert (bound to the session).
	cbSessRotate = callbackPrefix + "p:" + opRotate
	cbSessKeep   = callbackPrefix + "p:" + opKeep
	cbSessDrop   = callbackPrefix + "p:" + opDrop
	cbSessMerge  = callbackPrefix + "p:" + opMerge
)

// Prefixes of the buttons that work without a /convert session.
const (
	cbDocPrefix   = callbackPrefix + "doc:"  // conv:doc:<docID> opens the menu
	cbQuickPrefix = callbackPrefix + "q:"    // conv:q:<docID>:<op> converts a document
	cbSavePrefix  = callbackPrefix + "save:" // conv:save:<token> saves the result
	cbSkipPrefix  = callbackPrefix + "skip:" // conv:skip:<token> keeps only the file
	cbRepAsk      = callbackPrefix + "rp:"   // conv:rp:<token> asks to replace the original
	cbRepYes      = callbackPrefix + "rpy:"  // conv:rpy:<token> confirms the replacement
	cbRepNo       = callbackPrefix + "rpn:"  // conv:rpn:<token> cancels it
	cbAlbumPrefix = callbackPrefix + "alb:"  // conv:alb:<token> merges an album into a PDF
)

// Limits of an album PDF.
const (
	maxAlbumPages = 20
	maxAlbumBytes = maxFileBytes // all pages together
)

// Quick operations of the menu.
const (
	opCompress1 = "c1"
	opCompress2 = "c2"
	opCompress5 = "c5"
	opPDF       = "pdf"
	opJPG       = "jpg" // WEBP -> JPEG, no compression
)

const (
	maxFileBytes = 20 << 20   // Telegram download limit for bots
	maxLimitKB   = 20 * 1_024 // largest custom target, in KB
	downloadWait = 30 * time.Second
)

// converter runs the /convert dialogue. State lives in sessions; the image work
// is done by platform/convert.
type converter struct {
	log      *slog.Logger
	sessions *sessionStore
	results  *resultStore
	albums   *albumStore
	now      func() time.Time
	http     *http.Client

	// inflight holds the menu messages (chatID:msgID) whose operation is running,
	// so a double tap on the same menu does the work once.
	inflightMu sync.Mutex
	inflight   map[string]struct{}
	// tipShown remembers who has seen the "send as file" tip (once per process).
	tipMu    sync.Mutex
	tipShown map[int64]bool
	// dialogue serializes the dialogue message of one user, so photos of an album
	// (parallel updates) leave exactly one live message.
	dialogue keyedMutex

	svc *docsbot.Service
	// renderSaved and sendAlreadySaved show what saving a result produced; the
	// docs adapter owns that rendering.
	renderSaved      func(ctx context.Context, b *bot.Bot, chatID int64, msgID int, user docsbot.UserID, saved docsbot.Saved)
	sendAlreadySaved func(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID)
	// sendReplaced shows the card of a document whose file was replaced.
	sendReplaced func(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID)
}

func newConverter(
	log *slog.Logger,
	svc *docsbot.Service,
	renderSaved func(context.Context, *bot.Bot, int64, int, docsbot.UserID, docsbot.Saved),
	sendAlreadySaved func(context.Context, *bot.Bot, int64, docsbot.UserID, docsbot.DocumentID),
	sendReplaced func(context.Context, *bot.Bot, int64, docsbot.UserID, docsbot.DocumentID),
) *converter {
	return &converter{
		log:              log,
		sessions:         newSessionStore(time.Now),
		results:          newResultStore(time.Now),
		albums:           newAlbumStore(time.Now),
		now:              time.Now,
		http:             &http.Client{Timeout: downloadWait},
		inflight:         map[string]struct{}{},
		tipShown:         map[int64]bool{},
		dialogue:         newKeyedMutex(),
		svc:              svc,
		renderSaved:      renderSaved,
		sendAlreadySaved: sendAlreadySaved,
		sendReplaced:     sendReplaced,
	}
}

// ownsCallback matches this feature's inline buttons.
func (c *converter) ownsCallback(update *models.Update) bool {
	return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, callbackPrefix)
}

// ownsMessage matches any message from a user with a live session.
func (c *converter) ownsMessage(update *models.Update) bool {
	user, ok := messageUser(update)
	return ok && c.sessions.has(user)
}

// start opens a session and offers the two actions.
func (c *converter) start(ctx context.Context, b *bot.Bot, update *models.Update) {
	user, ok := messageUser(update)
	if !ok {
		return
	}
	unlock := c.dialogue.lock(user)
	defer unlock()
	if prev, ok := c.sessions.live(user); ok && prev.msgID != 0 {
		c.stripKeyboard(ctx, b, update.Message.Chat.ID, prev.msgID)
	}
	c.sessions.start(user)
	c.sessions.setMsg(user, c.send(ctx, b, update.Message.Chat.ID, textConvertStart, actionKeyboard()))
}

// cancel leaves converter mode.
func (c *converter) cancel(ctx context.Context, b *bot.Bot, update *models.Update) {
	user, ok := messageUser(update)
	if !ok {
		return
	}
	text := textConvertNothing
	if sess, ok := c.sessions.claim(user, 0); ok {
		text = textConvertCancelled
		if sess.msgID != 0 {
			c.stripKeyboard(ctx, b, update.Message.Chat.ID, sess.msgID)
		}
	}
	reply(ctx, b, c.log, update.Message.Chat.ID, text)
}

// message routes an in-session message by content and step.
func (c *converter) message(ctx context.Context, b *bot.Bot, update *models.Update) {
	user, ok := messageUser(update)
	if !ok {
		return
	}
	sess, ok := c.sessions.live(user)
	if !ok {
		return
	}
	chatID := update.Message.Chat.ID

	switch {
	case len(update.Message.Photo) > 0 && awaitsPDF(sess.step):
		reply(ctx, b, c.log, chatID, textPDFNotPDF)
	case len(update.Message.Photo) > 0:
		c.onImage(ctx, b, update, largestPhoto(update.Message.Photo).FileID)
	case update.Message.Document != nil:
		c.onDocument(ctx, b, update)
	case update.Message.Text != "":
		c.onText(ctx, b, update, sess.step)
	default:
		reply(ctx, b, c.log, chatID, textConvertUnsupported)
	}
}

// onDocument accepts image documents (and PDFs while merging) and rejects the rest.
func (c *converter) onDocument(ctx context.Context, b *bot.Bot, update *models.Update) {
	doc := update.Message.Document
	if doc.MimeType == mimePDF {
		c.onPDFPart(ctx, b, update)
		return
	}
	user, _ := messageUser(update)
	if sess, ok := c.sessions.live(user); ok && awaitsPDF(sess.step) {
		reply(ctx, b, c.log, update.Message.Chat.ID, textPDFNotPDF)
		return
	}
	if !supportedImageMIME(doc.MimeType) {
		text := textConvertUnsupported
		if unsupportedImage(doc.MimeType, doc.FileName) {
			text = textConvertFormatNotSupported
		}
		reply(ctx, b, c.log, update.Message.Chat.ID, text)
		return
	}
	c.onImage(ctx, b, update, doc.FileID)
}

// onImage downloads an image and adds it to the current step.
func (c *converter) onImage(ctx context.Context, b *bot.Bot, update *models.Update, fileID string) {
	user, _ := messageUser(update)
	chatID := update.Message.Chat.ID

	sess, ok := c.sessions.live(user)
	if !ok {
		return
	}
	if sess.step != stepCollectPDF && sess.step != stepAwaitImage {
		reply(ctx, b, c.log, chatID, textConvertTapButton)
		return
	}

	data, err := c.download(ctx, b, fileID)
	if err != nil {
		c.log.Error("convert download image", "err", err)
		reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
		return
	}

	unlock := c.dialogue.lock(user)
	defer unlock()
	toCompress, pages := false, 0
	c.sessions.update(user, func(s *session) {
		switch s.step {
		case stepAwaitImage:
			s.images = []pendingImage{{msgID: update.Message.ID, data: data}}
			s.step = stepChooseLimit
			s.inputPNG = isPNG(data)
			s.sourceTitle = ""
			if d := update.Message.Document; d != nil {
				s.sourceTitle = d.FileName
			}
			toCompress = true
		case stepCollectPDF:
			s.images = append(s.images, pendingImage{msgID: update.Message.ID, data: data})
			pages = len(s.images)
		}
	})
	switch {
	case toCompress:
		text := textConvertChooseLimit + c.fileTip(user, update)
		c.moveDown(ctx, b, chatID, user, text, limitKeyboard())
	case pages > 0 && !c.dialogue.busy(user):
		// Photos of an album arrive in parallel: only the last one waiting shows
		// the total, so the chat gets one message, not one per photo.
		text := fmt.Sprintf(textConvertPDFAccepted, pages) + c.fileTip(user, update)
		c.moveDown(ctx, b, chatID, user, text, pdfKeyboard())
	}
}

// fileTip is the "send as file" tip for a user who just sent a compressed photo
// (not a file). It is returned once per user per process, "" afterwards.
func (c *converter) fileTip(user int64, update *models.Update) string {
	if len(update.Message.Photo) == 0 {
		return ""
	}
	c.tipMu.Lock()
	defer c.tipMu.Unlock()
	if c.tipShown[user] {
		return ""
	}
	c.tipShown[user] = true
	return "\n\n" + textConvertFileTip
}

// pngMagic opens every PNG file.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func isPNG(data []byte) bool { return bytes.HasPrefix(data, pngMagic) }

// awaitsPDF reports whether the step waits for a PDF file from the user.
func awaitsPDF(s step) bool { return s == stepMergePDF || s == stepAwaitPDF }

// onText reads a typed custom size; other text just nudges to tap a button.
func (c *converter) onText(ctx context.Context, b *bot.Bot, update *models.Update, at step) {
	chatID := update.Message.Chat.ID
	if at == stepAwaitRange {
		c.onRange(ctx, b, update)
		return
	}
	if at != stepCustomLimit {
		reply(ctx, b, c.log, chatID, textConvertTapButton)
		return
	}

	kb, ok := parseLimitKB(update.Message.Text)
	if !ok {
		reply(ctx, b, c.log, chatID, textConvertBadNumber)
		return
	}
	user, _ := messageUser(update)
	unlock := sync.OnceFunc(c.dialogue.lock(user))
	defer unlock()
	sess, ok := c.sessions.advance(user, 0, []step{stepCustomLimit}, func(s *session) {
		s.maxBytes = kb * 1_024
		s.step = stepChooseFormat
	})
	if !ok {
		return
	}
	if !sess.inputPNG {
		unlock() // compress takes the lock itself
		c.compress(ctx, b, chatID, user, 0, convert.JPEG)
		return
	}
	c.moveDown(ctx, b, chatID, user, textConvertChooseFormat, formatKeyboard())
}

// callback handles a button press.
func (c *converter) callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	c.answer(ctx, b, update.CallbackQuery.ID)

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
		reply(ctx, b, c.log, chatID, textConvertCancelled)
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

// stale answers a press that no longer applies (an old message, a wrong step,
// an ended session, a double tap) and strips the message's buttons. It does
// nothing else.
func (c *converter) stale(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	reply(ctx, b, c.log, chatID, textStale)
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
	if msgID > 0 {
		params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text}
		if kb != nil {
			params.ReplyMarkup = kb
		}
		_, err := b.EditMessageText(ctx, params)
		if err == nil || isNotModified(err) {
			return msgID
		}
		c.log.WarnContext(ctx, "edit dialogue message failed, sending a new one", "err", err)
	}
	var markup models.ReplyMarkup
	if kb != nil {
		markup = kb
	}
	if id := c.send(ctx, b, chatID, text, markup); id != 0 {
		return id
	}
	return msgID // both failed: keep the old dialogue message so the session survives
}

// isNotModified reports Telegram's answer to an edit that changes nothing. The Bot
// API has no error code for it, only the description text.
func isNotModified(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// moveDown shows the next step of a dialogue that a user's message advanced: a
// new message at the bottom of the chat, next to what the user just sent, then
// the previous dialogue message is removed. Callers hold the user's dialogue lock.
func (c *converter) moveDown(ctx context.Context, b *bot.Bot, chatID, user int64, text string, kb *models.InlineKeyboardMarkup) {
	prev := 0
	if sess, ok := c.sessions.live(user); ok {
		prev = sess.msgID
	}
	var markup models.ReplyMarkup
	if kb != nil {
		markup = kb
	}
	id := c.send(ctx, b, chatID, text, markup)
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
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID}); err != nil {
		c.stripKeyboard(ctx, b, chatID, msgID)
	}
}

// finish removes the progress message once the result is sent; if it can't be
// deleted it turns into a short "done" note without buttons.
func (c *converter) finish(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID}); err != nil {
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

// finishPDF renders the collected photos into a PDF. The session is claimed
// first, so a double «Готово» builds it once; it goes back when the user can
// retry. The dialogue lock covers the claim and the progress note, so a photo
// arriving at the same time can't move the dialogue under them.
func (c *converter) finishPDF(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64) {
	unlock := c.dialogue.lock(user)
	msgID := callbackMessageID(update)
	sess, ok := c.sessions.claim(user, msgID, stepCollectPDF)
	if !ok {
		unlock()
		c.stale(ctx, b, update, chatID)
		return
	}
	images := sess.orderedImages()
	if len(images) == 0 {
		unlock()
		c.retry(ctx, b, chatID, user, sess, msgID, textConvertNoImages, pdfKeyboard())
		return
	}
	msgID = c.show(ctx, b, chatID, msgID, textConvertProgress, nil)
	unlock()

	pdf, err := convert.ImagesToPDF(images)
	if err != nil {
		c.log.ErrorContext(ctx, "convert images to pdf", "err", err, "pages", len(images))
		c.retry(ctx, b, chatID, user, sess, msgID, convertErrorText(err), pdfKeyboard())
		return
	}

	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, user, name, pdf, "", suffixPDF) {
		c.retry(ctx, b, chatID, user, sess, msgID, textSendFailed, pdfKeyboard())
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

// finishCompress compresses the stored image to the chosen format. The session
// is claimed first (one run per tap); it goes back when the user can retry.
func (c *converter) finishCompress(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64, format convert.Format) {
	if !c.compress(ctx, b, chatID, user, callbackMessageID(update), format) {
		c.stale(ctx, b, update, chatID)
	}
}

// compress runs the compression for a session at the format step; pressMsg is the
// message a button was pressed on (0 when a typed size got here). It reports
// false if there was nothing to claim. Callers must not hold the dialogue lock.
func (c *converter) compress(ctx context.Context, b *bot.Bot, chatID, user int64, pressMsg int, format convert.Format) bool {
	unlock := c.dialogue.lock(user)
	sess, ok := c.sessions.claim(user, pressMsg, stepChooseFormat)
	if !ok {
		unlock()
		return false
	}
	if len(sess.images) == 0 {
		c.sessions.restore(user, sess)
		unlock()
		return false
	}
	src := sess.images[0].data
	maxBytes := sess.maxBytes
	msgID := c.show(ctx, b, chatID, sess.msgID, textConvertProgress, nil)
	unlock()

	// An error puts the user back at the last question that was asked.
	retryKB := formatKeyboard()
	if !sess.inputPNG {
		retryKB = limitKeyboard()
		sess.step = stepChooseLimit
	}
	out, err := convert.Compress(src, maxBytes, format)
	if err != nil {
		c.log.ErrorContext(ctx, "convert compress", "err", err)
		c.retry(ctx, b, chatID, user, sess, msgID, convertErrorText(err), retryKB)
		return true
	}

	if len(out) > maxBytes {
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	name := resultFileName(sess.sourceTitle, nameFallbackPhoto, nameTagCompressed, formatExt(format))
	if !c.sendResult(ctx, b, chatID, user, name, out, "", suffixCompressed) {
		c.retry(ctx, b, chatID, user, sess, msgID, textSendFailed, retryKB)
		return true
	}
	c.finish(ctx, b, chatID, msgID)
	return true
}

// scanName is the file name of a PDF made from photos: "Скан 10.11.2026.pdf".
func scanName(now time.Time) string { return fmt.Sprintf(nameScan, now.Format("02.01.2006")) }

// loadDoc reads the user's document (the domain checks ownership) and answers
// the user itself when it can't.
func (c *converter) loadDoc(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, bool) {
	card, err := c.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		reply(ctx, b, c.log, chatID, textGone)
		return docsbot.Document{}, false
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert load document", "err", err, "user_id", user, "doc_id", id)
		reply(ctx, b, c.log, chatID, textError)
		return docsbot.Document{}, false
	}
	return card.Doc, true
}

// onMenu offers the operations that fit the document's type.
func (c *converter) onMenu(ctx context.Context, b *bot.Bot, chatID int64, id docsbot.DocumentID) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	switch {
	case isPDF(doc):
		sendMarkup(ctx, b, c.log, chatID, textConvertMenu, pdfMenuKeyboard(id))
	case convertible(doc):
		sendMarkup(ctx, b, c.log, chatID, textConvertMenu, quickKeyboard(id, isWebP(doc)))
	default:
		reply(ctx, b, c.log, chatID, unsupportedText(doc))
	}
}

// onQuick converts a stored document straight from the menu: conv:q:<docID>:<op>.
func (c *converter) onQuick(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	rest := strings.TrimPrefix(update.CallbackQuery.Data, cbQuickPrefix)
	rawID, op, ok := strings.Cut(rest, ":")
	if !ok || rawID == "" {
		return
	}
	user, ok := userID(ctx)
	if !ok {
		return
	}
	release, ok := c.claimMenu(chatID, callbackMessageID(update))
	if !ok {
		// The running tap owns the message: only tell the user, keep its buttons.
		reply(ctx, b, c.log, chatID, textStale)
		return
	}
	defer release()
	id := docsbot.DocumentID(rawID)

	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	if !convertible(doc) {
		reply(ctx, b, c.log, chatID, unsupportedText(doc))
		return
	}
	if isPDF(doc) {
		c.onPDFOp(ctx, b, update, chatID, doc, op)
		return
	}
	maxBytes, known := quickLimit(op)
	if !known && op != opPDF && (op != opJPG || !isWebP(doc)) {
		return
	}
	menu := quickKeyboard(id, isWebP(doc))
	msgID := c.show(ctx, b, chatID, callbackMessageID(update), textConvertProgress, nil)
	src, err := c.download(ctx, b, doc.TgFileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", id)
		c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, menu)
		return
	}

	var out []byte
	var name, suffix string
	switch op {
	case opPDF:
		suffix = suffixPDF
		out, err = convert.ImagesToPDF([][]byte{src})
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagPDF, "pdf")
	case opJPG:
		suffix = suffixJPG
		out, err = convert.ToJPEG(src)
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagJPG, "jpg")
	default:
		suffix = suffixCompressed
		out, err = convert.Compress(src, maxBytes, convert.JPEG)
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagCompressed, "jpg")
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert document", "err", err, "user_id", user, "doc_id", id, "op", op)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), menu)
		return
	}
	c.log.InfoContext(ctx, "document converted", "user_id", user, "doc_id", id, "op", op,
		"in_bytes", len(src), "out_bytes", len(out))
	if known && len(out) > maxBytes {
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	if !c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, out, id, suffix) {
		c.show(ctx, b, chatID, msgID, textSendFailed, menu)
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

// onSave stores the result as a document: conv:save:<token>.
func (c *converter) onSave(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbSavePrefix)
	res, ok := c.results.take(token, update.CallbackQuery.From.ID)
	if !ok {
		reply(ctx, b, c.log, chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, update, chatID)

	rep, err := c.svc.SaveDerived(ctx, res.user, res.sourceDoc, res.file, res.suffix)
	if errors.Is(err, docsbot.ErrNotFound) {
		reply(ctx, b, c.log, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "save converted file", "err", err, "user_id", res.user)
		reply(ctx, b, c.log, chatID, textError)
		return
	}

	switch r := rep.(type) {
	case docsbot.Saved:
		// The result message is a file, which can't be edited into a card: the
		// card replaces a fresh placeholder instead.
		msg, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: textConvertSaving})
		if err != nil {
			c.log.ErrorContext(ctx, "send telegram message", "err", err)
			return
		}
		c.renderSaved(ctx, b, chatID, msg.ID, res.user, r)
	case docsbot.NeedCategory:
		sendMarkup(ctx, b, c.log, chatID, textChooseCategory, categoryKeyboard(actCategory, r.Doc.ID))
	case docsbot.AlreadySaved:
		c.sendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		if text, ok := rejectionText(rep); ok {
			reply(ctx, b, c.log, chatID, text)
		}
	}
}

// onReplaceAsk turns the result's caption into a confirmation: conv:rp:<token>.
// The result stays in the store until the user confirms or cancels.
func (c *converter) onReplaceAsk(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepAsk)
	res, ok := c.results.peek(token, update.CallbackQuery.From.ID)
	if !ok || res.sourceDoc == "" {
		reply(ctx, b, c.log, chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, update, chatID, textReplaceConfirm, replaceKeyboard(token))
}

// onReplaceCancel puts the result's caption and buttons back: conv:rpn:<token>.
func (c *converter) onReplaceCancel(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepNo)
	if _, ok := c.results.peek(token, update.CallbackQuery.From.ID); !ok {
		reply(ctx, b, c.log, chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, update, chatID, "", saveKeyboard(token, true))
}

// onReplace swaps the source document's file for the result: conv:rpy:<token>.
// The result is taken first, so a double tap replaces once. When the document
// cannot be replaced right now the result is put back, so the user can try again.
func (c *converter) onReplace(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepYes)
	res, ok := c.results.take(token, update.CallbackQuery.From.ID)
	if !ok || res.sourceDoc == "" {
		reply(ctx, b, c.log, chatID, textConvertResultOld)
		return
	}
	retry := func(text string) {
		c.results.put(token, res)
		c.editResultCaption(ctx, b, update, chatID, "", saveKeyboard(token, true))
		reply(ctx, b, c.log, chatID, text)
	}

	rep, err := c.svc.ReplaceContent(ctx, res.user, res.sourceDoc, res.file)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		reply(ctx, b, c.log, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "replace original", "err", err, "user_id", res.user, "doc_id", res.sourceDoc)
		retry(textError)
		return
	}

	switch r := rep.(type) {
	case docsbot.ReplaceBusy:
		retry(textReplaceBusy)
	case docsbot.ReplaceMissing:
		retry(textReplaceMissing)
	case docsbot.Replaced:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		c.sendReplaced(ctx, b, chatID, res.user, r.Doc.ID)
	case docsbot.AlreadySaved:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		c.sendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		if text, ok := rejectionText(rep); ok {
			reply(ctx, b, c.log, chatID, text)
		}
	}
}

// editResultCaption sets the caption and buttons (nil removes them) of the
// result message a button was pressed on, best effort.
func (c *converter) editResultCaption(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64, caption string, kb *models.InlineKeyboardMarkup) {
	m := update.CallbackQuery.Message.Message
	if m == nil {
		return
	}
	if kb == nil {
		kb = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	}
	_, err := b.EditMessageCaption(ctx, &bot.EditMessageCaptionParams{
		ChatID: chatID, MessageID: m.ID, Caption: caption, ReplyMarkup: kb,
	})
	if err != nil && !isNotModified(err) {
		c.log.ErrorContext(ctx, "edit result caption", "err", err)
	}
}

// registerAlbum remembers an album's images for the "one PDF" button and returns
// the button's token.
func (c *converter) registerAlbum(user docsbot.UserID, group string, images []albumImage) string {
	sorted := slices.Clone(images)
	slices.SortFunc(sorted, func(a, b albumImage) int { return a.msgID - b.msgID })
	token := newToken()
	c.albums.put(token, convAlbum{user: user, group: group, images: sorted})
	return token
}

// onAlbum merges an album into one PDF: conv:alb:<token>. On success the album's
// pending documents are dropped, so they don't wait for a category.
func (c *converter) onAlbum(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbAlbumPrefix)
	user, ok := userID(ctx)
	if !ok {
		return
	}
	album, ok := c.albums.peek(token, user)
	if !ok {
		reply(ctx, b, c.log, chatID, textConvertResultOld)
		return
	}
	var declared int64
	for _, img := range album.images {
		declared += img.size
	}
	switch {
	case len(album.images) > maxAlbumPages:
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textAlbumTooMany, maxAlbumPages)) // the buttons stay: save one by one
		return
	case declared > maxAlbumBytes:
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20))
		return
	}
	album, ok = c.albums.take(token, user) // a double tap stops here
	if !ok {
		return
	}

	// The choice message becomes the progress note; an error puts its buttons back.
	var choice *models.InlineKeyboardMarkup
	if m := update.CallbackQuery.Message.Message; m != nil {
		choice = m.ReplyMarkup
	}
	msgID := c.show(ctx, b, chatID, callbackMessageID(update), textConvertProgress, nil)
	pages := make([][]byte, 0, len(album.images))
	var total int
	for _, img := range album.images {
		data, err := c.download(ctx, b, img.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download album image", "err", err, "user_id", user)
			c.albums.put(token, album) // let the user retry
			c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, choice)
			return
		}
		if total += len(data); total > maxAlbumBytes {
			c.albums.put(token, album)
			c.show(ctx, b, chatID, msgID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20), choice)
			return
		}
		pages = append(pages, data)
	}
	pdf, err := convert.ImagesToPDF(pages)
	if err != nil {
		c.log.ErrorContext(ctx, "convert album to pdf", "err", err, "user_id", user, "pages", len(pages))
		c.albums.put(token, album)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), choice)
		return
	}
	c.log.InfoContext(ctx, "album converted", "user_id", user, "pages", len(pages), "in_bytes", total, "out_bytes", len(pdf))

	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, pdf, "", suffixPDF) {
		c.show(ctx, b, chatID, msgID, textSendFailed, choice)
		c.albums.put(token, album)
		return
	}
	c.finish(ctx, b, chatID, msgID)
	n, err := c.svc.DiscardAlbum(ctx, user, album.group)
	if err != nil {
		c.log.ErrorContext(ctx, "discard album", "err", err, "user_id", user)
		return
	}
	c.log.InfoContext(ctx, "album discarded", "user_id", user, "count", n)
}

// onSkip keeps only the file: conv:skip:<token>.
func (c *converter) onSkip(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	c.results.take(strings.TrimPrefix(update.CallbackQuery.Data, cbSkipPrefix), update.CallbackQuery.From.ID)
	c.clearButtons(ctx, b, update, chatID)
}

// clearButtons removes the keyboard from the message a button was pressed on.
func (c *converter) clearButtons(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	if m := update.CallbackQuery.Message.Message; m != nil {
		c.stripKeyboard(ctx, b, chatID, m.ID)
	}
}

// stripKeyboard removes the keyboard from a message, best effort.
func (c *converter) stripKeyboard(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	_, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: msgID, ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	})
	if err != nil {
		c.log.ErrorContext(ctx, "clear buttons", "err", err)
	}
}

// deleteCallbackMessage removes the message a button was pressed on.
func (c *converter) deleteCallbackMessage(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	m := update.CallbackQuery.Message.Message
	if m == nil {
		return
	}
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: m.ID}); err != nil {
		c.log.ErrorContext(ctx, "delete message", "err", err)
	}
}

// sendResult uploads the converted file with the save / just-the-file buttons
// and reports whether Telegram accepted it. The choice is remembered only if
// Telegram echoes the stored file back; if it doesn't, the buttons are removed
// again, since they could not work. Without the user's internal id the file goes
// out without buttons.
func (c *converter) sendResult(ctx context.Context, b *bot.Bot, chatID, tgUser int64, filename string, data []byte,
	sourceDoc docsbot.DocumentID, suffix string,
) bool {
	params := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
	}
	user, haveUser := userID(ctx)
	token := newToken()
	if haveUser {
		params.ReplyMarkup = saveKeyboard(token, sourceDoc != "")
	}
	msg, err := b.SendDocument(ctx, params)
	if err != nil {
		c.log.ErrorContext(ctx, "send telegram document", "err", err)
		return false
	}
	if !haveUser {
		return true
	}
	if msg == nil || msg.Document == nil {
		c.removeKeyboard(ctx, b, chatID, msg)
		return true
	}
	c.results.put(token, convResult{
		tgUser: tgUser, user: user, sourceDoc: sourceDoc, suffix: suffix,
		file: docsbot.IncomingFile{
			Kind: docsbot.KindDocument, FileID: msg.Document.FileID, FileUniqueID: msg.Document.FileUniqueID,
			Title: docsbot.TrimExt(filename), FileSize: msg.Document.FileSize, MIME: msg.Document.MimeType,
		},
	})
	return true
}

// removeKeyboard strips the buttons from a sent message, when there is one.
func (c *converter) removeKeyboard(ctx context.Context, b *bot.Bot, chatID int64, msg *models.Message) {
	if msg != nil {
		c.stripKeyboard(ctx, b, chatID, msg.ID)
	}
}

// download fetches a Telegram file's bytes, capped at maxFileBytes.
func (c *converter) download(ctx context.Context, b *bot.Bot, fileID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadWait)
	defer cancel()

	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.FileDownloadLink(file), nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", stripURL(err))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", stripURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) > maxFileBytes {
		return nil, errors.New("file exceeds download limit")
	}
	return data, nil
}

// stripURL drops the request URL from a transport error: the download link
// carries the bot token, and the error ends up in the logs.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// answer stops the button's loading spinner.
func (c *converter) answer(ctx context.Context, b *bot.Bot, callbackID string) {
	if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID}); err != nil {
		c.log.Error("answer callback", "err", err)
	}
}

// convertErrorText maps a convert error to a user message.
func convertErrorText(err error) string {
	switch {
	case errors.Is(err, convert.ErrImageTooLarge):
		return textConvertTooLarge
	case errors.Is(err, convert.ErrUnsupportedFormat):
		return textConvertUnsupported
	default:
		return textConvertFailed
	}
}

// parseLimitKB reads a positive size in KB, up to maxLimitKB.
func parseLimitKB(text string) (int, bool) {
	kb, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || kb <= 0 || kb > maxLimitKB {
		return 0, false
	}
	return kb, true
}

// limitBytes maps a preset limit button to bytes.
func limitBytes(data string) int {
	switch data {
	case cbLimit1:
		return 1 << 20
	case cbLimit2:
		return 2 << 20
	case cbLimit5:
		return 5 << 20
	default:
		return 0
	}
}

func formatExt(f convert.Format) string {
	if f == convert.PNG {
		return "png"
	}
	return "jpg"
}

// convertible reports whether the converter can work with the document: a photo,
// a JPEG/PNG/WEBP file or a PDF.
func convertible(doc docsbot.Document) bool {
	if doc.Kind == docsbot.KindPhoto {
		return true
	}
	return doc.Kind == docsbot.KindDocument && (supportedImageMIME(doc.MIME) || isPDF(doc))
}

// isWebP reports whether the document is a WEBP image.
func isWebP(doc docsbot.Document) bool {
	return doc.Kind == docsbot.KindDocument && doc.MIME == "image/webp"
}

// mimePDF is the MIME type of a PDF document.
const mimePDF = "application/pdf"

// isPDF reports whether the document is a PDF file.
func isPDF(doc docsbot.Document) bool {
	return doc.Kind == docsbot.KindDocument && doc.MIME == mimePDF
}

// supportedImageMIME reports whether we can decode this MIME type.
func supportedImageMIME(mime string) bool {
	switch mime {
	case "image/jpeg", "image/jpg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

// unsupportedImage reports a modern image format we know but can't decode:
// HEIC/HEIF/AVIF, by MIME type or, when the MIME is missing or generic, by file
// extension.
func unsupportedImage(mime, name string) bool {
	switch mime {
	case "image/heic", "image/heif", "image/heic-sequence", "image/heif-sequence", "image/avif":
		return true
	case "", "application/octet-stream":
		switch strings.ToLower(filepath.Ext(name)) {
		case ".heic", ".heif", ".avif":
			return true
		}
	}
	return false
}

// unsupportedText is the refusal for a stored document the converter can't take.
func unsupportedText(doc docsbot.Document) string {
	if unsupportedImage(doc.MIME, doc.Title) {
		return textConvertFormatNotSupported
	}
	return textConvertUnsupported
}

// largestPhoto returns the highest-resolution size (Telegram sorts ascending).
func largestPhoto(sizes []models.PhotoSize) models.PhotoSize {
	return sizes[len(sizes)-1]
}

func messageUser(update *models.Update) (int64, bool) {
	if update.Message == nil || update.Message.From == nil {
		return 0, false
	}
	return update.Message.From.ID, true
}

// callbackMessageID is the id of the message a button was pressed on, 0 if unknown.
func callbackMessageID(update *models.Update) int {
	if m := update.CallbackQuery.Message.Message; m != nil {
		return m.ID
	}
	return 0
}

func callbackChat(update *models.Update) (int64, bool) {
	if update.CallbackQuery.Message.Message == nil {
		return 0, false
	}
	return update.CallbackQuery.Message.Message.Chat.ID, true
}

// sendMarkup sends text with an inline keyboard.
func sendMarkup(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string, markup models.ReplyMarkup) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup}); err != nil {
		log.Error("send telegram message", "err", err)
	}
}

// send is sendMarkup that returns the sent message's id (0 on failure).
func (c *converter) send(ctx context.Context, b *bot.Bot, chatID int64, text string, markup models.ReplyMarkup) int {
	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup})
	if err != nil || msg == nil {
		if err != nil {
			c.log.ErrorContext(ctx, "send telegram message", "err", err)
		}
		return 0
	}
	return msg.ID
}

func actionKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnPDF, CallbackData: cbPDF},
			{Text: btnCompress, CallbackData: cbCompress},
		},
		{{Text: btnPDFActions, CallbackData: cbPDFActions}},
	}}
}

func pdfKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnDone, CallbackData: cbDone},
		{Text: btnCancel, CallbackData: cbCancel},
	}}}
}

func limitKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnLimit1MB, CallbackData: cbLimit1},
			{Text: btnLimit2MB, CallbackData: cbLimit2},
			{Text: btnLimit5MB, CallbackData: cbLimit5},
		},
		{
			{Text: btnLimitCustom, CallbackData: cbLimitCustom},
			{Text: btnCancel, CallbackData: cbCancel},
		},
	}}
}

func formatKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnJPEG, CallbackData: cbFmtJPEG},
		{Text: btnPNG, CallbackData: cbFmtPNG},
	}}}
}

func cancelKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnCancel, CallbackData: cbCancel},
	}}}
}

// quickKeyboard is the menu of an image; a WEBP also gets a plain conversion to JPG.
func quickKeyboard(id docsbot.DocumentID, webp bool) *models.InlineKeyboardMarkup {
	q := func(op string) string { return cbQuickPrefix + string(id) + ":" + op }
	second := []models.InlineKeyboardButton{{Text: btnQuickPDF, CallbackData: q(opPDF)}}
	if webp {
		second = append(second, models.InlineKeyboardButton{Text: btnQuickJPG, CallbackData: q(opJPG)})
	}
	second = append(second, models.InlineKeyboardButton{Text: btnQuickCancel, CallbackData: cbMenuCancel})
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnQuick1MB, CallbackData: q(opCompress1)},
			{Text: btnQuick2MB, CallbackData: q(opCompress2)},
			{Text: btnQuick5MB, CallbackData: q(opCompress5)},
		},
		second,
	}}
}

// saveKeyboard is the result's keyboard. A result made from a stored document
// (replace) can also replace that document's file.
func saveKeyboard(token string, replace bool) *models.InlineKeyboardMarkup {
	if replace {
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: btnSaveAsNew, CallbackData: cbSavePrefix + token}},
			{{Text: btnReplaceOriginal, CallbackData: cbRepAsk + token}},
			{{Text: btnSkipResult, CallbackData: cbSkipPrefix + token}},
		}}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: btnSaveResult, CallbackData: cbSavePrefix + token}},
		{{Text: btnSkipResult, CallbackData: cbSkipPrefix + token}},
	}}
}

// replaceKeyboard confirms replacing the original.
func replaceKeyboard(token string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnReplaceYes, CallbackData: cbRepYes + token},
		{Text: btnReplaceNo, CallbackData: cbRepNo + token},
	}}}
}

// quickLimit maps a compress operation of the menu to its size in bytes.
func quickLimit(op string) (int, bool) {
	switch op {
	case opCompress1:
		return 1 << 20, true
	case opCompress2:
		return 2 << 20, true
	case opCompress5:
		return 5 << 20, true
	default:
		return 0, false
	}
}

// resultFileName is the file name of a converted document: the title without its
// extension (fallback when there is none), a dash, the tag and the extension:
// "Договор-сжатый.jpg". Path separators are replaced so the name is a single path
// element.
func resultFileName(title, fallback, tag, ext string) string {
	base := docsbot.DerivedTitle(title, "") // drops the extension, trims the length
	if base == "" {
		base = fallback
	}
	base = strings.NewReplacer("/", "_", "\\", "_").Replace(base)
	return base + "-" + tag + "." + ext
}
