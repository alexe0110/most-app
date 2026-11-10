package telegram_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// sentDocument is a document the bot uploaded.
type sentDocument struct {
	filename string
	data     []byte
}

// convHarness is a fake Bot API wired to a docsbot client. It serves file
// downloads and records what the bot sends back.
type convHarness struct {
	bot     *bot.Bot
	mu      sync.Mutex
	texts   []string
	markups int
	// markupBodies are the reply markups of sent messages, in order.
	markupBodies []string
	docs         []sentDocument
	repo         *fakeRepo
	// docMarkups are the reply markups of the sent documents, in order.
	docMarkups []string
	// edits counts editMessageReplyMarkup calls (buttons removed).
	edits int
	// downloads lists the file ids the bot fetched, in order.
	downloads []string
	// files overrides what a file id downloads; the default is a small JPEG.
	files map[string][]byte
	// noDocument makes sendDocument answer without the stored document.
	noDocument bool
	// editTexts are the texts of editMessageText calls, in order, as "id:text".
	editTexts []string
	// editCaptions are the editMessageCaption calls, in order, as "id:caption|markup".
	editCaptions []string
	// deleted lists the message ids the bot deleted.
	deleted []int
	// failEdits makes editMessageText answer with an error (message can't be edited).
	failEdits bool
	// gate, when set, holds every file download until it is closed.
	gate chan struct{}
	// failDocs makes sendDocument answer with an error (after recording the upload).
	failDocs bool
	// failSends makes sendMessage answer with an error.
	failSends bool
	// failDeletes makes deleteMessage answer with an error (message too old).
	failDeletes bool
	// events is the order of sends ("send:<text>"), edits ("edit:<id>:<text>") and
	// deletions ("del:<id>").
	events []string
	// nextMsg is the id of the next sent message (sendMessage answers with it).
	nextMsg int
}

// serve makes fileID download as data.
func (h *convHarness) serve(fileID string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[fileID] = data
}

func newConvHarness(t *testing.T, opts ...telegram.Option) *convHarness {
	t.Helper()
	h := &convHarness{files: map[string][]byte{}, nextMsg: 100}

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
			id := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".jpg")
			h.mu.Lock()
			h.downloads = append(h.downloads, id)
			data, ok := h.files[id]
			gate := h.gate
			h.mu.Unlock()
			if gate != nil {
				<-gate
			}
			if !ok {
				data = photo.Bytes()
			}
			_, _ = w.Write(data)
			return
		case strings.HasSuffix(path, "/getFile"):
			var req struct {
				FileID string `json:"file_id"`
			}
			req.FileID = r.FormValue("file_id")
			if req.FileID == "" {
				_ = json.NewDecoder(r.Body).Decode(&req)
			}
			writeJSON(w, fmt.Sprintf(`{"ok":true,"result":{"file_id":"f","file_path":"%s.jpg"}}`, req.FileID))
			return
		case strings.HasSuffix(path, "/sendDocument"):
			n := h.captureDocument(t, r)
			if h.docsFail() {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
				return
			}
			if h.noDocument {
				writeJSON(w, `{"ok":true,"result":{"message_id":7}}`)
				return
			}
			writeJSON(w, fmt.Sprintf(`{"ok":true,"result":{"message_id":7,"document":{"file_id":"res-%[1]d","file_unique_id":"res-uniq-%[1]d","file_size":1234,"mime_type":"image/jpeg"}}}`, n))
			return
		case strings.HasSuffix(path, "/editMessageReplyMarkup"):
			h.mu.Lock()
			h.edits++
			h.mu.Unlock()
			writeJSON(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(path, "/editMessageCaption"):
			h.mu.Lock()
			h.editCaptions = append(h.editCaptions, r.FormValue("message_id")+":"+r.FormValue("caption")+"|"+r.FormValue("reply_markup"))
			h.mu.Unlock()
			writeJSON(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(path, "/editMessageText"):
			h.mu.Lock()
			fail := h.failEdits
			if !fail {
				h.editTexts = append(h.editTexts, r.FormValue("message_id")+":"+r.FormValue("text"))
				h.events = append(h.events, "edit:"+r.FormValue("message_id")+":"+r.FormValue("text"))
			}
			h.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, `{"ok":false,"error_code":400,"description":"Bad Request: message can't be edited"}`)
				return
			}
			writeJSON(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(path, "/deleteMessage"):
			id, _ := strconv.Atoi(r.FormValue("message_id"))
			h.mu.Lock()
			if h.failDeletes {
				h.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, `{"ok":false,"error_code":400,"description":"Bad Request: message can't be deleted"}`)
				return
			}
			h.deleted = append(h.deleted, id)
			h.events = append(h.events, "del:"+r.FormValue("message_id"))
			h.mu.Unlock()
			writeJSON(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(path, "/sendMessage") && h.sendsFail():
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`)
			return
		case strings.HasSuffix(path, "/sendMessage"):
			h.captureMessage(r)
			h.mu.Lock()
			h.nextMsg++
			id := h.nextMsg
			h.mu.Unlock()
			writeJSON(w, fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, id))
			return
		default: // answerCallbackQuery, etc.
			writeJSON(w, `{"ok":true,"result":{}}`)
		}
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)
	svc, auth, repo := fakeDeps()
	telegram.Register(b, slog.New(slog.DiscardHandler), svc, auth, opts...)

	h.bot = b
	h.repo = repo
	return h
}

func (h *convHarness) docsFail() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failDocs
}

func (h *convHarness) sendsFail() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failSends
}

func (h *convHarness) captureMessage(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.texts = append(h.texts, r.FormValue("text"))
	h.events = append(h.events, "send:"+r.FormValue("text"))
	h.markupBodies = append(h.markupBodies, r.FormValue("reply_markup"))
	if r.FormValue("reply_markup") != "" {
		h.markups++
	}
}

// captureDocument records an upload and returns its 0-based index.
func (h *convHarness) captureDocument(t *testing.T, r *http.Request) int {
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
	h.docMarkups = append(h.docMarkups, r.FormValue("reply_markup"))
	return len(h.docs) - 1
}

// lastShown is the text of the last sent message or edit.
func (h *convHarness) lastShown() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.events) - 1; i >= 0; i-- {
		switch {
		case strings.HasPrefix(h.events[i], "send:"):
			return strings.TrimPrefix(h.events[i], "send:")
		case strings.HasPrefix(h.events[i], "edit:"):
			_, text, _ := strings.Cut(strings.TrimPrefix(h.events[i], "edit:"), ":")
			return text
		}
	}
	return ""
}

func (h *convHarness) lastText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.texts) == 0 {
		return ""
	}
	return h.texts[len(h.texts)-1]
}

// albumMarkup is the reply markup of the first sent message.
func (h *convHarness) albumMarkup() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.markupBodies) == 0 {
		return ""
	}
	return h.markupBodies[0]
}

func (h *convHarness) lastMarkup() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.markupBodies) == 0 {
		return ""
	}
	return h.markupBodies[len(h.markupBodies)-1]
}

func (h *convHarness) allTexts() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.texts, "\n")
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

// callbackOn is callbackUpdate for a press on the message with the given id.
func callbackOn(data string, msgID int) *models.Update {
	u := callbackUpdate(data)
	u.CallbackQuery.Message.Message.ID = msgID
	return u
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
	require.True(t, strings.HasPrefix(doc.filename, "Скан "), "got %q", doc.filename)
	require.True(t, strings.HasSuffix(doc.filename, ".pdf"))
	require.True(t, bytes.HasPrefix(doc.data, []byte("%PDF")))
	require.Contains(t, string(doc.data), "/Count 3", "one page per photo")
	require.Empty(t, h.repo.docs, "a result is not saved unless asked")
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
	require.Equal(t, "фото-сжатый.jpg", h.docs[0].filename)
	require.LessOrEqual(t, len(h.docs[0].data), 2<<20)
	_, _, err := image.Decode(bytes.NewReader(h.docs[0].data))
	require.NoError(t, err)
	require.Empty(t, h.repo.docs, "a result is not saved unless asked")
}

func TestConvert_compressCustomLimit(t *testing.T) {
	h := newConvHarness(t)
	h.servePNG("large")
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limitcustom"))
	h.bot.ProcessUpdate(ctx, convText("300"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtpng"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "фото-сжатый.png", h.docs[0].filename)
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
	h.bot.ProcessUpdate(ctx, documentUpdate("application/zip"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "пока не умею")
}

func TestConvert_cancelReturnsToMainScenario(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:cancel"))

	// With the session gone, an unknown command falls through to the catch-all
	// (plain text is now a search, which needs a stored user).
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "foobar"))
	require.Contains(t, h.lastText(), "Не понимаю")
}
