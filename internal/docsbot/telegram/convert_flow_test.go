package telegram_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// sentDocument records a document the bot uploaded to the fake Bot API.
type sentDocument struct {
	filename string
	data     []byte
}

// convHarness is a fake Telegram Bot API wired to a docsbot client. It serves
// getFile + file downloads so the converter can fetch image bytes, and records
// the messages and documents the bot sends back.
type convHarness struct {
	bot     *bot.Bot
	mu      sync.Mutex
	texts   []string
	markups int
	docs    []sentDocument
}

func newConvHarness(t *testing.T) *convHarness {
	t.Helper()
	h := &convHarness{}

	// The image every file download serves — a small, decodable JPEG.
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 5), 120, 255})
		}
	}
	var photo bytes.Buffer
	require.NoError(t, jpeg.Encode(&photo, img, &jpeg.Options{Quality: 85}))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/file/"):
			_, _ = w.Write(photo.Bytes())
			return
		case strings.HasSuffix(path, "/getFile"):
			writeJSON(w, `{"ok":true,"result":{"file_id":"f","file_path":"photo.jpg"}}`)
			return
		case strings.HasSuffix(path, "/sendDocument"):
			h.captureDocument(t, r)
			writeJSON(w, `{"ok":true,"result":{}}`)
			return
		case strings.HasSuffix(path, "/sendMessage"):
			h.captureMessage(r)
			writeJSON(w, `{"ok":true,"result":{}}`)
			return
		default: // answerCallbackQuery, etc.
			writeJSON(w, `{"ok":true,"result":{}}`)
		}
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)
	telegram.Register(b, slog.New(slog.DiscardHandler))

	h.bot = b
	return h
}

func (h *convHarness) captureMessage(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.texts = append(h.texts, r.FormValue("text"))
	if r.FormValue("reply_markup") != "" {
		h.markups++
	}
}

func (h *convHarness) captureDocument(t *testing.T, r *http.Request) {
	t.Helper()
	require.NoError(t, r.ParseMultipartForm(32<<20))
	file, header, err := r.FormFile("document")
	require.NoError(t, err)
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	require.NoError(t, err)

	h.mu.Lock()
	defer h.mu.Unlock()
	h.docs = append(h.docs, sentDocument{filename: header.Filename, data: data})
}

func (h *convHarness) lastText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.texts) == 0 {
		return ""
	}
	return h.texts[len(h.texts)-1]
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

const (
	testUser = int64(42)
	testChat = int64(42)
)

func photoUpdate(msgID int) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:   msgID,
		From: &models.User{ID: testUser},
		Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Photo: []models.PhotoSize{
			{FileID: "small", Width: 90, Height: 60},
			{FileID: "large", Width: 1280, Height: 960},
		},
	}}
}

func documentUpdate(mime string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:       1,
		From:     &models.User{ID: testUser},
		Chat:     models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "doc", MimeType: mime},
	}}
}

func convText(text string) *models.Update {
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: testUser},
		Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func callbackUpdate(data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: testUser},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{Chat: models.Chat{ID: testChat, Type: models.ChatTypePrivate}},
		},
	}}
}

func TestConvert_photosToPDF(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	for i := 1; i <= 3; i++ {
		h.bot.ProcessUpdate(ctx, photoUpdate(i))
	}
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	doc := h.docs[0]
	require.True(t, strings.HasPrefix(doc.filename, "scan-"), "got %q", doc.filename)
	require.True(t, strings.HasSuffix(doc.filename, ".pdf"))
	require.True(t, bytes.HasPrefix(doc.data, []byte("%PDF")))
	require.Contains(t, string(doc.data), "/Count 3", "one page per photo")
}

func TestConvert_compressPreset(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "photo-compressed.jpg", h.docs[0].filename)
	require.LessOrEqual(t, len(h.docs[0].data), 2<<20)
	_, _, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
}

func TestConvert_compressCustomLimit(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("300"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtpng"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "photo-compressed.png", h.docs[0].filename)
}

func TestConvert_customLimitRejectsJunk(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("не число"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "число")
}

func TestConvert_unsupportedDocument(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, documentUpdate("image/heic"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "пока не умею")
}

func TestConvert_cancelReturnsToMainScenario(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:cancel"))

	// With the session gone, a plain message falls through to the catch-all.
	h.bot.ProcessUpdate(ctx, convText("привет"))
	require.Contains(t, h.lastText(), "Не понимаю")
}
