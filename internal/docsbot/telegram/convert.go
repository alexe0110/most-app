package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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
	now      func() time.Time
	http     *http.Client
}

func newConverter(log *slog.Logger) *converter {
	return &converter{
		log:      log,
		sessions: newSessionStore(time.Now),
		now:      time.Now,
		http:     &http.Client{Timeout: downloadWait},
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
	sendMarkup(ctx, b, c.log, update.Message.Chat.ID, textConvertStart, actionKeyboard())
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

// onDocument accepts image documents and rejects the rest (HEIC, PDF, …).
func (c *converter) onDocument(ctx context.Context, b *bot.Bot, update *models.Update) {
	doc := update.Message.Document
	if !supportedImageMIME(doc.MimeType) {
		reply(ctx, b, c.log, update.Message.Chat.ID, textConvertUnsupported)
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
		c.log.Error("convert download image", "err", err, "chat_id", chatID)
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
		sendMarkup(ctx, b, c.log, chatID, textConvertChooseLimit, limitKeyboard())
	}
	// PDF mode collects silently; [Готово] builds the result.
}

// onText reads a typed custom size; other text just nudges to tap a button.
func (c *converter) onText(ctx context.Context, b *bot.Bot, update *models.Update, step step) {
	chatID := update.Message.Chat.ID
	if step != stepCustomLimit {
		reply(ctx, b, c.log, chatID, textConvertTapButton)
		return
	}

	kb, ok := parseLimitKB(update.Message.Text)
	if !ok {
		reply(ctx, b, c.log, chatID, textConvertBadNumber)
		return
	}
	user, _ := messageUser(update)
	c.sessions.update(user, func(s *session) {
		s.maxBytes = kb * 1_024
		s.step = stepChooseFormat
	})
	sendMarkup(ctx, b, c.log, chatID, textConvertChooseFormat, formatKeyboard())
}

// callback handles a button press.
func (c *converter) callback(ctx context.Context, b *bot.Bot, update *models.Update) {
	c.answer(ctx, b, update.CallbackQuery.ID)

	chatID, ok := callbackChat(update)
	if !ok {
		return
	}
	user := update.CallbackQuery.From.ID
	if !c.sessions.has(user) {
		reply(ctx, b, c.log, chatID, textConvertExpired)
		return
	}

	switch update.CallbackQuery.Data {
	case cbPDF:
		c.sessions.update(user, func(s *session) { s.step = stepCollectPDF })
		sendMarkup(ctx, b, c.log, chatID, textConvertPDFPrompt, pdfKeyboard())
	case cbCompress:
		c.sessions.update(user, func(s *session) { s.step = stepAwaitImage })
		sendMarkup(ctx, b, c.log, chatID, textConvertCompressPrompt, cancelKeyboard())
	case cbDone:
		c.finishPDF(ctx, b, user, chatID)
	case cbCancel:
		c.sessions.end(user)
		reply(ctx, b, c.log, chatID, textConvertCancelled)
	case cbLimit1, cbLimit2, cbLimit5:
		c.sessions.update(user, func(s *session) {
			s.maxBytes = limitBytes(update.CallbackQuery.Data)
			s.step = stepChooseFormat
		})
		sendMarkup(ctx, b, c.log, chatID, textConvertChooseFormat, formatKeyboard())
	case cbLimitCustom:
		c.sessions.update(user, func(s *session) { s.step = stepCustomLimit })
		reply(ctx, b, c.log, chatID, textConvertCustomLimit)
	case cbFmtJPEG:
		c.finishCompress(ctx, b, user, chatID, convert.JPEG)
	case cbFmtPNG:
		c.finishCompress(ctx, b, user, chatID, convert.PNG)
	}
}

// finishPDF renders the collected photos into a PDF.
func (c *converter) finishPDF(ctx context.Context, b *bot.Bot, user, chatID int64) {
	sess, ok := c.sessions.live(user)
	if !ok || sess.step != stepCollectPDF {
		return
	}
	images := sess.orderedImages()
	if len(images) == 0 {
		reply(ctx, b, c.log, chatID, textConvertNoImages)
		return
	}

	pdf, err := convert.ImagesToPDF(images)
	if err != nil {
		c.log.Error("convert images to pdf", "err", err, "chat_id", chatID, "pages", len(images))
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}

	c.sessions.end(user)
	name := "scan-" + c.now().Format("2006-01-02") + ".pdf"
	sendDocument(ctx, b, c.log, chatID, name, pdf)
}

// finishCompress compresses the stored image to the chosen format.
func (c *converter) finishCompress(ctx context.Context, b *bot.Bot, user, chatID int64, format convert.Format) {
	sess, ok := c.sessions.live(user)
	if !ok || sess.step != stepChooseFormat || len(sess.images) == 0 {
		return
	}
	src := sess.images[0].data
	maxBytes := sess.maxBytes

	out, err := convert.Compress(src, maxBytes, format)
	if err != nil {
		c.log.Error("convert compress", "err", err, "chat_id", chatID)
		reply(ctx, b, c.log, chatID, convertErrorText(err))
		return
	}

	c.sessions.end(user)
	if len(out) > maxBytes {
		reply(ctx, b, c.log, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	name := "photo-compressed." + formatExt(format)
	sendDocument(ctx, b, c.log, chatID, name, out)
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
		return nil, fmt.Errorf("build download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
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

// supportedImageMIME reports whether we can decode this MIME type.
func supportedImageMIME(mime string) bool {
	switch mime {
	case "image/jpeg", "image/jpg", "image/png":
		return true
	default:
		return false
	}
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

func callbackChat(update *models.Update) (int64, bool) {
	if update.CallbackQuery.Message.Message == nil {
		return 0, false
	}
	return update.CallbackQuery.Message.Message.Chat.ID, true
}

// sendMarkup sends text with an inline keyboard.
func sendMarkup(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string, markup models.ReplyMarkup) {
	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: markup}); err != nil {
		log.Error("send telegram message", "err", err, "chat_id", chatID)
	}
}

// sendDocument uploads data as a file.
func sendDocument(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, filename string, data []byte) {
	_, err := b.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
	})
	if err != nil {
		log.Error("send telegram document", "err", err, "chat_id", chatID)
	}
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
