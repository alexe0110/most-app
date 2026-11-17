package telegram_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// snapshot copies a capture bucket under the lock (handlers may run concurrently).
func (c *capture) snapshot(bucket *[]string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), *bucket...)
}

// capture records what the bot sent to the fake Bot API, split by method.
type capture struct {
	mu          sync.Mutex
	texts       []string     // sendMessage
	edits       []string     // editMessageText
	markups     []string     // sendMessage reply_markup
	editMarkups []string     // editMessageText reply_markup
	files       []string     // sendDocument/sendPhoto caption
	deletes     int          // deleteMessage calls
	deleteIDs   []int        // message_id of each deleteMessage
	editIDs     []int        // message_id of each editMessageText
	answers     []string     // answerCallbackQuery toast texts
	groups      []string     // sendMediaGroup "media" JSON, one per album
	markupEdits []string     // editMessageReplyMarkup reply_markup
	mediaLimit  int          // the next N sendMediaGroup calls answer 429 (retry_after 0)
	mediaDown   bool         // every sendMediaGroup fails with 400
	sendModes   []string     // sendMessage parse_mode
	editModes   []string     // editMessageText parse_mode
	inlines     []url.Values // answerInlineQuery form fields
	forbidden   bool         // message calls answer 403 "bot was blocked by the user"
	refused     []string     // methods refused with 403, in order
	sendChats   []string     // sendMessage chat_id, parallel to texts
	copies      []string     // copyMessage/copyMessages as "to<-from:ids", in order
	blockedChat string       // sendMessage/copyMessage to this chat_id answer 403
	copyDown    bool         // copyMessage(s) fail with 400
}

// messageMethods are the calls a blocked user makes Telegram refuse.
var messageMethods = []string{"sendMessage", "editMessageText", "editMessageReplyMarkup", "deleteMessage", "answerCallbackQuery"}

// refuse answers 403 to a message call while forbidden is set, and reports whether it did.
func (c *capture) refuse(w http.ResponseWriter, r *http.Request) bool {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	c.mu.Lock()
	defer c.mu.Unlock()
	toBlocked := c.blockedChat != "" && r.FormValue("chat_id") == c.blockedChat &&
		(method == "sendMessage" || method == "copyMessage" || method == "copyMessages")
	if !toBlocked && (!c.forbidden || !slices.Contains(messageMethods, method)) {
		return false
	}
	c.refused = append(c.refused, method)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	return true
}

func (c *capture) copyFailing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.copyDown
}

// copyMany records a copyMessages call and answers with one new id per message.
func (c *capture) copyMany(w http.ResponseWriter, r *http.Request) {
	c.record(&c.copies, r.FormValue("chat_id")+"<-"+r.FormValue("from_chat_id")+":"+r.FormValue("message_ids"))
	var ids []int
	_ = json.Unmarshal([]byte(r.FormValue("message_ids")), &ids)
	result := make([]map[string]int, len(ids))
	for i := range ids {
		result[i] = map[string]int{"message_id": copiedMessageID + i}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (c *capture) record(bucket *[]string, s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*bucket = append(*bucket, s)
}

// formInt reads an integer form field of a Bot API request, 0 when absent.
func formInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.FormValue(key))
	return n
}

// has reports whether any captured message contains sub.
func has(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// newBot stands up a fake Bot API and a docsbot client over in-memory fakes.
// Handlers run synchronously, so a test can assert right after ProcessUpdate.
func newBot(t *testing.T) (*bot.Bot, *capture, *docsbottest.FakeRepo) {
	t.Helper()
	return newBotWithLog(t, slog.New(slog.DiscardHandler))
}

// newBotWithLog is newBot with a caller-supplied logger.
func newBotWithLog(t *testing.T, log *slog.Logger) (*bot.Bot, *capture, *docsbottest.FakeRepo) {
	t.Helper()
	return newBotWith(t, log, docsbottest.FakeDrive{})
}

// newBotWith is newBotWithLog with a chosen Drive state.
func newBotWith(t *testing.T, log *slog.Logger, drive docsbottest.FakeDrive) (*bot.Bot, *capture, *docsbottest.FakeRepo) {
	t.Helper()
	return newBotOpts(t, log, drive)
}

// newBotOpts is newBotWith with adapter options.
func newBotOpts(t *testing.T, log *slog.Logger, drive docsbottest.FakeDrive, opts ...telegram.Option) (*bot.Bot, *capture, *docsbottest.FakeRepo) {
	t.Helper()
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sent.refuse(w, r) {
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent.record(&sent.texts, r.FormValue("text"))
			sent.record(&sent.sendChats, r.FormValue("chat_id"))
			sent.record(&sent.sendModes, r.FormValue("parse_mode"))
			sent.record(&sent.markups, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/sendDocument"), strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			sent.record(&sent.files, r.FormValue("caption"))
		case strings.HasSuffix(r.URL.Path, "/editMessageReplyMarkup"):
			sent.record(&sent.markupEdits, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/sendMediaGroup"):
			sent.mu.Lock()
			limited, down := sent.mediaLimit > 0, sent.mediaDown
			if limited {
				sent.mediaLimit--
			}
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if limited {
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 0","parameters":{"retry_after":0}}`)
				return
			}
			if down {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier"}`)
				return
			}
			sent.record(&sent.groups, r.FormValue("media"))
			_, _ = io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			sent.mu.Lock()
			sent.deletes++
			sent.deleteIDs = append(sent.deleteIDs, formInt(r, "message_id"))
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(r.URL.Path, "/copyMessage"), strings.HasSuffix(r.URL.Path, "/copyMessages"):
			if sent.copyFailing() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message to copy not found"}`)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/copyMessages") {
				sent.copyMany(w, r)
				return
			}
			sent.record(&sent.copies, r.FormValue("chat_id")+"<-"+r.FormValue("from_chat_id")+":"+r.FormValue("message_id"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":`+strconv.Itoa(copiedMessageID)+`}}`)
			return
		case strings.HasSuffix(r.URL.Path, "/answerInlineQuery"):
			_ = r.ParseMultipartForm(1 << 20)
			sent.mu.Lock()
			sent.inlines = append(sent.inlines, r.Form)
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			sent.record(&sent.answers, r.FormValue("text"))
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			sent.record(&sent.edits, r.FormValue("text"))
			sent.record(&sent.editModes, r.FormValue("parse_mode"))
			sent.mu.Lock()
			sent.editIDs = append(sent.editIDs, formInt(r, "message_id"))
			sent.mu.Unlock()
			sent.record(&sent.editMarkups, r.FormValue("reply_markup"))
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":`+strconv.Itoa(sentMessageID)+`}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)

	svc, repo := docsbottest.NewDeps(drive)
	telegram.Register(b, telegram.Deps{Log: log, Svc: svc, Auth: docsbottest.FakeAuth{}, Files: docsbottest.NoFiles{}}, opts...)
	return b, sent, repo
}

// sentMessageID is the message id the fake API gives every sent message.
const sentMessageID = 777

// copiedMessageID is the message id the fake API gives the first copy made by copyMessage(s).
const copiedMessageID = 901

func commandUpdate(cmd string) *models.Update {
	const chatID = 100
	text := "/" + cmd
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: chatID},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
		Entities: []models.MessageEntity{
			{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len(text)},
		},
	}}
}

// dataCallback is a button tap by user 100 on message 7.
func dataCallback(data string) *models.Update {
	return dataCallbackMsg(7, data)
}

// dataCallbackMsg is dataCallback from the message with the given id.
func dataCallbackMsg(msgID int, data string) *models.Update {
	const chatID = 100
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: chatID},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: msgID, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}},
		},
	}}
}

// inaccessible is a press on a message Telegram no longer gives (too old).
func inaccessible(u *models.Update) *models.Update {
	u.CallbackQuery.Message = models.MaybeInaccessibleMessage{
		Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
		InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
	}
	return u
}

// textUpdateID is textUpdate from user 100 with a message id (the bot deletes renamed-title messages).
func textUpdateID(msgID int, text string) *models.Update {
	u := textUpdate(text)
	u.Message.ID = msgID
	return u
}

// textUpdate is a text message from user 100 in their private chat.
func textUpdate(text string) *models.Update {
	const chatID = 100
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: chatID},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func seedPassport(repo *docsbottest.FakeRepo, title, uid string) docsbot.Document {
	return repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
	})
}

// lockedBuffer is a log sink safe for concurrent handlers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// docsbottest.FakeDrive reports a configurable connection state; by default "not connected",
