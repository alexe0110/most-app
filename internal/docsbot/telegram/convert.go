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
)

// Prefixes of the buttons that work without a /convert session.
const (
	cbDocPrefix   = callbackPrefix + "doc:"  // conv:doc:<docID> opens the menu
	cbQuickPrefix = callbackPrefix + "q:"    // conv:q:<docID>:<op> converts a document
	cbSavePrefix  = callbackPrefix + "save:" // conv:save:<token> saves the result
	cbSkipPrefix  = callbackPrefix + "skip:" // conv:skip:<token> keeps only the file
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

	svc *docsbot.Service
	// renderSaved and sendAlreadySaved show what saving a result produced; the
	// docs adapter owns that rendering.
	renderSaved      func(ctx context.Context, b *bot.Bot, chatID int64, msgID int, user docsbot.UserID, saved docsbot.Saved)
	sendAlreadySaved func(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID)
}

func newConverter(
	log *slog.Logger,
	svc *docsbot.Service,
	renderSaved func(context.Context, *bot.Bot, int64, int, docsbot.UserID, docsbot.Saved),
	sendAlreadySaved func(context.Context, *bot.Bot, int64, docsbot.UserID, docsbot.DocumentID),
) *converter {
	return &converter{
		log:              log,
		sessions:         newSessionStore(time.Now),
		results:          newResultStore(time.Now),
		albums:           newAlbumStore(time.Now),
		now:              time.Now,
		http:             &http.Client{Timeout: downloadWait},
		inflight:         map[string]struct{}{},
		svc:              svc,
		renderSaved:      renderSaved,
		sendAlreadySaved: sendAlreadySaved,
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
	if c.sessions.end(user) {
		text = textConvertCancelled
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
	case len(update.Message.Photo) > 0 && sess.step == stepMergePDF:
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
	if sess, ok := c.sessions.live(user); ok && sess.step == stepMergePDF {
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

	toCompress := false
	c.sessions.update(user, func(s *session) {
		switch s.step {
		case stepAwaitImage:
			s.images = []pendingImage{{msgID: update.Message.ID, data: data}}
			s.step = stepChooseLimit
			toCompress = true
		case stepCollectPDF:
			s.images = append(s.images, pendingImage{msgID: update.Message.ID, data: data})
		}
	})
	if toCompress {
		c.sessions.setMsg(user, c.send(ctx, b, chatID, textConvertChooseLimit, limitKeyboard()))
	}
	// PDF mode collects silently; [Готово] builds the result.
}

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
	if _, ok := c.sessions.advance(user, 0, []step{stepCustomLimit}, func(s *session) {
		s.maxBytes = kb * 1_024
		s.step = stepChooseFormat
	}); !ok {
		return
	}
	c.sessions.setMsg(user, c.send(ctx, b, chatID, textConvertChooseFormat, formatKeyboard()))
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
		c.onMenu(ctx, b, chatID, docsbot.DocumentID(strings.TrimPrefix(data, cbDocPrefix)))
		return
	case strings.HasPrefix(data, cbQuickPrefix):
		c.onQuick(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbSavePrefix):
		c.onSave(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbAlbumPrefix):
		c.onAlbum(ctx, b, update, chatID)
		return
	case strings.HasPrefix(data, cbSkipPrefix):
		c.onSkip(ctx, b, update, chatID)
		return
	case data == cbMenuCancel:
		c.deleteCallbackMessage(ctx, b, update, chatID)
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
		if _, ok := c.sessions.advance(user, msgID, []step{stepChooseLimit}, func(s *session) {
			s.maxBytes = limitBytes(data)
			s.step = stepChooseFormat
		}); !ok {
			c.stale(ctx, b, update, chatID)
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

// next moves the dialogue on after a button press: the pressed message loses its
// buttons and the next step goes out as the session's dialogue message.
func (c *converter) next(ctx context.Context, b *bot.Bot, update *models.Update, chatID, user int64, text string, kb *models.InlineKeyboardMarkup) {
	c.clearButtons(ctx, b, update, chatID)
	var markup models.ReplyMarkup
	if kb != nil {
		markup = kb
	}
	c.sessions.setMsg(user, c.send(ctx, b, chatID, text, markup))
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
// retry.
func (c *converter) finishPDF(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64) {
	sess, ok := c.sessions.claim(user, callbackMessageID(update), stepCollectPDF)
	if !ok {
		c.stale(ctx, b, update, chatID)
		return
	}
	images := sess.orderedImages()
	if len(images) == 0 {
		c.sessions.restore(user, sess)
		reply(ctx, b, c.log, chatID, textConvertNoImages)
		return
	}

	pdf, err := convert.ImagesToPDF(images)
	if err != nil {
		c.log.Error("convert images to pdf", "err", err, "pages", len(images))
		c.sessions.restore(user, sess)
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}

	c.clearButtons(ctx, b, update, chatID)
	name := scanName(c.now())
	c.sendResult(ctx, b, chatID, user, name, pdf, "", suffixPDF)
}

// finishCompress compresses the stored image to the chosen format. The session
// is claimed first (one run per tap); it goes back when the user can retry.
func (c *converter) finishCompress(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64, format convert.Format) {
	sess, ok := c.sessions.claim(user, callbackMessageID(update), stepChooseFormat)
	if !ok || len(sess.images) == 0 {
		c.stale(ctx, b, update, chatID)
		return
	}
	src := sess.images[0].data
	maxBytes := sess.maxBytes

	out, err := convert.Compress(src, maxBytes, format)
	if err != nil {
		c.log.Error("convert compress", "err", err)
		c.sessions.restore(user, sess)
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}

	c.clearButtons(ctx, b, update, chatID)
	if len(out) > maxBytes {
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	name := "photo-compressed." + formatExt(format)
	c.sendResult(ctx, b, chatID, user, name, out, "", suffixCompressed)
}

// scanName is the file name of a PDF made from photos.
func scanName(now time.Time) string { return "scan-" + now.Format("2006-01-02") + ".pdf" }

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
		c.stale(ctx, b, update, chatID)
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
		c.onPDFOp(ctx, b, update, chatID, user, doc, op)
		return
	}
	maxBytes, known := quickLimit(op)
	if !known && op != opPDF && (op != opJPG || !isWebP(doc)) {
		return
	}
	src, err := c.download(ctx, b, doc.TgFileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", id)
		reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
		return
	}

	var out []byte
	var name, suffix string
	switch op {
	case opPDF:
		suffix = suffixPDF
		out, err = convert.ImagesToPDF([][]byte{src})
		name = resultName(doc.Title, suffix, "scan", "pdf")
	case opJPG:
		suffix = suffixJPG
		out, err = convert.ToJPEG(src)
		name = resultName(doc.Title, suffix, "photo", "jpg")
	default:
		suffix = suffixCompressed
		out, err = convert.Compress(src, maxBytes, convert.JPEG)
		name = resultName(doc.Title, suffix, "photo-compressed", "jpg")
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert document", "err", err, "user_id", user, "doc_id", id, "op", op)
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "document converted", "user_id", user, "doc_id", id, "op", op,
		"in_bytes", len(src), "out_bytes", len(out))
	if known && len(out) > maxBytes {
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	c.clearButtons(ctx, b, update, chatID)
	c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, out, id, suffix)
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

	pages := make([][]byte, 0, len(album.images))
	var total int
	for _, img := range album.images {
		data, err := c.download(ctx, b, img.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download album image", "err", err, "user_id", user)
			c.albums.put(token, album) // let the user retry
			reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
			return
		}
		if total += len(data); total > maxAlbumBytes {
			c.albums.put(token, album)
			reply(ctx, b, c.log, chatID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20))
			return
		}
		pages = append(pages, data)
	}
	pdf, err := convert.ImagesToPDF(pages)
	if err != nil {
		c.log.ErrorContext(ctx, "convert album to pdf", "err", err, "user_id", user, "pages", len(pages))
		c.albums.put(token, album)
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "album converted", "user_id", user, "pages", len(pages), "in_bytes", total, "out_bytes", len(pdf))

	c.clearButtons(ctx, b, update, chatID)
	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, pdf, "", suffixPDF) {
		return
	}
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
		params.ReplyMarkup = saveKeyboard(token)
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
			Title: filename, FileSize: msg.Document.FileSize, MIME: msg.Document.MimeType,
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
			c.log.Error("send telegram message", "err", err)
		}
		return 0
	}
	return msg.ID
}

func actionKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnPDF, CallbackData: cbPDF},
		{Text: btnCompress, CallbackData: cbCompress},
	}}}
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

func saveKeyboard(token string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: btnSaveResult, CallbackData: cbSavePrefix + token}},
		{{Text: btnSkipResult, CallbackData: cbSkipPrefix + token}},
	}}
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

// resultName is the file name of a converted document: its derived title plus
// the extension, or fallback when the document has no title. Path separators
// are replaced so the name is a single path element.
func resultName(title, suffix, fallback, ext string) string {
	base := fallback
	if strings.TrimSpace(title) != "" {
		base = docsbot.DerivedTitle(title, suffix)
	}
	base = strings.NewReplacer("/", "_", "\\", "_").Replace(base)
	return base + "." + ext
}
