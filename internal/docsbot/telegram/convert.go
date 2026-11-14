package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// callbackPrefix tags every inline button this feature owns.
const callbackPrefix = "conv:"

// Callback data for the inline buttons that carry no argument.
const (
	cbPDF         = callbackPrefix + convActPDF
	cbCompress    = callbackPrefix + convActCompress
	cbDone        = callbackPrefix + convActDone
	cbCancel      = callbackPrefix + convActCancel
	cbLimit1      = callbackPrefix + convActLimit1
	cbLimit2      = callbackPrefix + convActLimit2
	cbLimit5      = callbackPrefix + convActLimit5
	cbLimitCustom = callbackPrefix + convActLimitCustom
	cbFmtJPEG     = callbackPrefix + convActFmtJPEG
	cbFmtPNG      = callbackPrefix + convActFmtPNG
	cbMenuCancel  = callbackPrefix + convActMenuCancel
	cbPDFActions  = callbackPrefix + convActPDFOps

	// PDF operations on a PDF sent to /convert (bound to the session).
	cbSessRotate = callbackPrefix + convActPDFOp + ":" + opRotate
	cbSessKeep   = callbackPrefix + convActPDFOp + ":" + opKeep
	cbSessDrop   = callbackPrefix + convActPDFOp + ":" + opDrop
	cbSessMerge  = callbackPrefix + convActPDFOp + ":" + opMerge
)

// Limits of an album PDF.
const (
	maxAlbumPages = 20
	maxAlbumBytes = tg.MaxFileBytes // all pages together
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
	maxLimitKB   = 20 * 1_024 // largest custom target, in KB
	downloadWait = 30 * time.Second
	// photosDownloadBudget caps all downloads of one PDF from photos together.
	photosDownloadBudget = 80 * time.Second
)

// converter runs the /convert dialogue. State lives in sessions; the image work
// is done by platform/convert.
type converter struct {
	log      *slog.Logger
	sessions *sessionStore
	results  *tokenMap[convResult]
	albums   *tokenMap[convAlbum]
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
		results:          newTokenMap[convResult](time.Now),
		albums:           newTokenMap[convAlbum](time.Now),
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
		c.out(b).clearKeyboard(ctx, update.Message.Chat.ID, prev.msgID)
	}
	c.sessions.start(user)
	id := c.out(b).send(ctx, update.Message.Chat.ID, textConvertStart, withKeyboard(actionKeyboard())) // 0: no dialogue message
	c.sessions.setMsg(user, id)
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
			c.out(b).clearKeyboard(ctx, update.Message.Chat.ID, sess.msgID)
		}
	}
	c.out(b).send(ctx, update.Message.Chat.ID, text)
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
		c.out(b).send(ctx, chatID, textPDFNotPDF)
	case len(update.Message.Photo) > 0:
		photo := largestPhoto(update.Message.Photo)
		c.onImage(ctx, b, update, photo.FileID, int64(photo.FileSize))
	case update.Message.Document != nil:
		c.onDocument(ctx, b, update)
	case update.Message.Text != "":
		c.onText(ctx, b, update, sess.step)
	default:
		c.out(b).send(ctx, chatID, textConvertUnsupported)
	}
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
func limitBytes(action string) int {
	switch action {
	case convActLimit1:
		return 1 << 20
	case convActLimit2:
		return 2 << 20
	case convActLimit5:
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
