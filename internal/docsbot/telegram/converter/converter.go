// Package converter is the /convert inbound adapter of docsbot: the dialogue that
// turns photos into a PDF, compresses an image or edits a PDF, and the convert
// menu of stored documents. The work itself is done by platform/convert; the docs
// adapter renders what saving a result produced.
package converter

import (
	"context"
	"errors"
	"log/slog"
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
	maxLimitKB = 20 * 1_024 // largest custom target, in KB
	// photosDownloadBudget caps all downloads of one PDF from photos together.
	photosDownloadBudget = 80 * time.Second
	// imageTimeout bounds one image operation (decode, compress, build a PDF); like
	// pdfTimeout it keeps a pathological file from holding a worker.
	imageTimeout = 20 * time.Second
)

// FileDownloader fetches a Telegram file's bytes and content type by file_id.
// Declared here, at the consumer; platform/tg.Bot implements it.
type FileDownloader interface {
	Download(ctx context.Context, fileID string) (data []byte, mime string, err error)
}

// SavedView shows the docs side of a saved result: cards, the category picker and
// limit notices. Declared here, at the consumer; the docs adapter implements it.
type SavedView interface {
	// RenderSaved turns message msgID into the card of the saved document.
	RenderSaved(ctx context.Context, b *bot.Bot, chatID int64, msgID int, user docsbot.UserID, saved docsbot.Saved)
	SendAlreadySaved(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID)
	// SendReplaced shows the card of a document whose file was replaced.
	SendReplaced(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID)
	// SendNotice answers a reply the converter does not own: the category picker
	// of a new document or an intake limit.
	SendNotice(ctx context.Context, b *bot.Bot, chatID int64, rep docsbot.Reply)
}

// Deps are the required collaborators of the converter.
type Deps struct {
	Log   *slog.Logger
	Svc   *docsbot.Service
	Files FileDownloader
	Saved SavedView
}

// Converter runs the /convert dialogue. State lives in sessions; the image work
// is done by platform/convert.
type Converter struct {
	log      *slog.Logger
	sessions *sessionStore
	results  *tokenMap[convResult]
	albums   *tokenMap[convAlbum]
	cleaned  *tokenMap[cleanedCopy]
	now      func() time.Time
	files    FileDownloader

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
	// docs shows what saving a result produced; the docs adapter owns that rendering.
	docs SavedView
}

// New makes the converter.
func New(deps Deps) *Converter {
	return &Converter{
		log:      deps.Log,
		sessions: newSessionStore(time.Now),
		results:  newTokenMap[convResult](time.Now),
		albums:   newTokenMap[convAlbum](time.Now),
		cleaned:  newTokenMap[cleanedCopy](time.Now),
		now:      time.Now,
		files:    deps.Files,
		inflight: map[string]struct{}{},
		tipShown: map[int64]bool{},
		dialogue: newKeyedMutex(),
		svc:      deps.Svc,
		docs:     deps.Saved,
	}
}

// Register wires the /convert and /cancel commands and the converter's buttons
// and in-session messages onto b. Call it after the docs commands and before the
// docs catch-all handlers: the first matching handler wins, and a user with a
// live session gets all their messages here.
func (c *Converter) Register(b *bot.Bot) {
	b.RegisterHandler(bot.HandlerTypeMessageText, "convert", bot.MatchTypeCommand, c.start)
	b.RegisterHandler(bot.HandlerTypeMessageText, "cancel", bot.MatchTypeCommand, c.cancel)
	b.RegisterHandlerMatchFunc(c.ownsCallback, c.callback)
	b.RegisterHandlerMatchFunc(c.ownsMessage, c.message)
}

// ownsCallback matches this feature's inline buttons.
func (c *Converter) ownsCallback(update *models.Update) bool {
	return update.CallbackQuery != nil && strings.HasPrefix(update.CallbackQuery.Data, callbackPrefix)
}

// ownsMessage matches any message from a user with a live session.
func (c *Converter) ownsMessage(update *models.Update) bool {
	user, ok := messageUser(update)
	return ok && c.sessions.has(user)
}

// start opens a session and offers the two actions.
func (c *Converter) start(ctx context.Context, b *bot.Bot, update *models.Update) {
	user, ok := messageUser(update)
	if !ok {
		return
	}
	unlock := c.dialogue.lock(user)
	defer unlock()
	if prev, ok := c.sessions.live(user); ok && prev.msgID != 0 {
		c.out(b).ClearKeyboard(ctx, update.Message.Chat.ID, prev.msgID)
	}
	if !c.sessions.start(user) {
		c.out(b).Send(ctx, update.Message.Chat.ID, textConvertBusy)
		return
	}
	id := c.out(b).Send(ctx, update.Message.Chat.ID, textConvertStart, tg.WithKeyboard(actionKeyboard())) // 0: no dialogue message
	c.sessions.setMsg(user, id)
}

// cancel leaves converter mode.
func (c *Converter) cancel(ctx context.Context, b *bot.Bot, update *models.Update) {
	user, ok := messageUser(update)
	if !ok {
		return
	}
	text := textConvertNothing
	if sess, ok := c.sessions.claim(user, 0); ok {
		text = textConvertCancelled
		if sess.msgID != 0 {
			c.out(b).ClearKeyboard(ctx, update.Message.Chat.ID, sess.msgID)
		}
	}
	c.out(b).Send(ctx, update.Message.Chat.ID, text)
}

// message routes an in-session message by content and step.
func (c *Converter) message(ctx context.Context, b *bot.Bot, update *models.Update) {
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
		c.out(b).Send(ctx, chatID, textPDFNotPDF)
	case len(update.Message.Photo) > 0:
		photo := largestPhoto(update.Message.Photo)
		c.onImage(ctx, b, update, photo.FileID, int64(photo.FileSize))
	case update.Message.Document != nil:
		c.onDocument(ctx, b, update)
	case update.Message.Text != "":
		c.onText(ctx, b, update, sess.step)
	default:
		c.out(b).Send(ctx, chatID, textConvertUnsupported)
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

// Convertible reports whether the converter can work with the document: a photo,
// a JPEG/PNG/WEBP file or a PDF.
func Convertible(doc docsbot.Document) bool {
	if doc.Kind == docsbot.KindPhoto {
		return true
	}
	return doc.Kind == docsbot.KindDocument && (SupportedImageMIME(doc.MIME) || IsPDF(doc))
}

// isWebP reports whether the document is a WEBP image.
func isWebP(doc docsbot.Document) bool {
	return doc.Kind == docsbot.KindDocument && doc.MIME == "image/webp"
}

// MIME types of the documents the converter tells apart.
const (
	mimePDF = "application/pdf"
	mimePNG = "image/png"
)

// IsPDF reports whether the document is a PDF file.
func IsPDF(doc docsbot.Document) bool {
	return doc.Kind == docsbot.KindDocument && doc.MIME == mimePDF
}

// SupportedImageMIME reports whether we can decode this MIME type.
func SupportedImageMIME(mime string) bool {
	switch mime {
	case "image/jpeg", "image/jpg", mimePNG, "image/webp":
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
