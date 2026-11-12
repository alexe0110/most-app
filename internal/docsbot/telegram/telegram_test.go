package telegram_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// capture records what the bot sent to the fake Bot API, split by method.
type capture struct {
	mu          sync.Mutex
	texts       []string // sendMessage
	edits       []string // editMessageText
	markups     []string // sendMessage reply_markup
	editMarkups []string // editMessageText reply_markup
	files       []string // sendDocument/sendPhoto caption
	deletes     int      // deleteMessage calls
	deleteIDs   []int    // message_id of each deleteMessage
	editIDs     []int    // message_id of each editMessageText
	answers     []string // answerCallbackQuery toast texts
	groups      []string // sendMediaGroup "media" JSON, one per album
	markupEdits []string // editMessageReplyMarkup reply_markup
	mediaLimit  int      // the next N sendMediaGroup calls answer 429 (retry_after 0)
	mediaDown   bool     // every sendMediaGroup fails with 400
	sendModes   []string // sendMessage parse_mode
	editModes   []string // editMessageText parse_mode
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
func newBot(t *testing.T) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotWithLog(t, slog.New(slog.DiscardHandler))
}

// newBotWithLog is newBot with a caller-supplied logger.
func newBotWithLog(t *testing.T, log *slog.Logger) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotWith(t, log, fakeDrive{})
}

// newBotWith is newBotWithLog with a chosen Drive state.
func newBotWith(t *testing.T, log *slog.Logger, drive fakeDrive) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	return newBotOpts(t, log, drive)
}

// newBotOpts is newBotWith with adapter options.
func newBotOpts(t *testing.T, log *slog.Logger, drive fakeDrive, opts ...telegram.Option) (*bot.Bot, *capture, *fakeRepo) {
	t.Helper()
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent.record(&sent.texts, r.FormValue("text"))
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
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)

	svc, auth, repo := fakeDepsWith(drive)
	telegram.Register(b, log, svc, auth, opts...)
	return b, sent, repo
}

const testUserID = "11111111-1111-1111-1111-111111111111"

func commandUpdate(chatID int64, cmd string) *models.Update {
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

func TestRegister_routing(t *testing.T) {
	tests := []struct {
		name     string
		update   *models.Update
		wantText string
	}{
		{"start", commandUpdate(100, "start"), "Most Docs"},
		{"help", commandUpdate(100, "help"), "умею"},
		{"unknown command", commandUpdate(100, "foo"), "Не понимаю"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBot(t)
			b.ProcessUpdate(t.Context(), tt.update)
			require.True(t, has(sent.texts, tt.wantText), "sent %q", sent.texts)
		})
	}
}

func TestReceive_asksCategory(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	update := &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}}
	b.ProcessUpdate(ctx, update)

	require.True(t, has(sent.texts, "категори"), "sent %q", sent.texts)
}

func TestReceive_duplicateShowsFolderButton(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	repo.folders = map[docsbot.Category]string{docsbot.CatOther: "fld-1"}
	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1",
	})
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})

	require.True(t, has(sent.texts, "Паспорт"), "sent %q", sent.texts)
	require.True(t, has(sent.markups, "https://drive.google.com/drive/folders/fld-1"), "markups %q", sent.markups)
}

func TestReceive_duplicateBackedUpLinksFileOnly(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	repo.folders = map[docsbot.Category]string{docsbot.CatOther: "fld-1"}
	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1",
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "drive-1",
	})
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})

	require.True(t, has(sent.markups, "https://drive.google.com/file/d/drive-1/view"), "markups %q", sent.markups)
	require.False(t, has(sent.markups, "/drive/folders/"), "markups %q", sent.markups)
}

func TestChooseCategory_savesAndShowsCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
		Title: "Паспорт", TgFileID: "f1", TgFileUniqueID: "u1", BackupStatus: docsbot.BackupNone,
	})
	update := &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb1",
		From: models.User{ID: 100},
		Data: "d:cat:" + string(doc.ID) + ":" + string(docsbot.CatOther),
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		},
	}}
	b.ProcessUpdate(ctx, update)

	require.False(t, has(sent.texts, "Сохранил"), "the picker becomes the card, no extra message")
	require.Len(t, sent.texts, 1, "only the Drive nudge for the first document: %q", sent.texts)
	require.True(t, has(sent.edits, "🗂 "), "edits %q", sent.edits)
	require.False(t, has(sent.edits, "✅"), "edits %q", sent.edits)
	require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
	require.Equal(t, docsbot.CatOther, repo.docs[doc.ID].Category)
}

func dataCallback(chatID int64, data string) *models.Update {
	return dataCallbackMsg(chatID, 7, data)
}

// dataCallbackMsg is dataCallback from the message with the given id.
func dataCallbackMsg(chatID int64, msgID int, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: chatID},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: msgID, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}},
		},
	}}
}

// textUpdateID is textUpdate with a message id (the bot deletes renamed-title messages).
func textUpdateID(chatID int64, msgID int, text string) *models.Update {
	u := textUpdate(chatID, text)
	u.Message.ID = msgID
	return u
}

func textUpdate(chatID int64, text string) *models.Update {
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: chatID},
		Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		Text: text,
	}}
}

func TestSettings_showsCountsAndControls(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "p", TgFileUniqueID: "u1"})
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "o", TgFileUniqueID: "u2"})

	b.ProcessUpdate(ctx, commandUpdate(100, "settings"))

	require.True(t, has(sent.texts, "Google Drive"), "sent %q", sent.texts)
	require.True(t, has(sent.texts, "Документов: 2"), "sent %q", sent.texts)
}

func TestDeleteData_confirmation(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	// Tapping "Удалить мои данные" asks for the exact word.
	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))
	require.True(t, has(sent.texts, "УДАЛИТЬ"), "prompt asks for the word: %q", sent.texts)

	// A wrong word cancels and leaves the data in place.
	b.ProcessUpdate(ctx, textUpdate(100, "удалить"))
	require.True(t, has(sent.texts, "отменено"), "sent %q", sent.texts)
	require.Len(t, repo.docs, 1, "data intact after a wrong word")

	// Re-arm, then the exact word deletes everything.
	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))
	b.ProcessUpdate(ctx, textUpdate(100, "УДАЛИТЬ"))
	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Empty(t, repo.docs, "data deleted after the exact word")
}

func TestSearch_findsSavedDocument(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	update := &models.Update{Message: &models.Message{
		From: &models.User{ID: 100},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Text: "паспорт",
	}}
	b.ProcessUpdate(ctx, update)

	require.True(t, has(sent.texts, "Нашёл"), "sent %q", sent.texts)
}

func TestOpenCard_editsMessageWithBack(t *testing.T) {
	tests := []struct {
		name     string
		suffix   string
		wantBack string
	}{
		{"from list", ":l:" + string(docsbot.CatPassport) + ":1", "d:lc:" + string(docsbot.CatPassport) + ":1"},
		{"from search", ":s", "d:sb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{
				UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
				Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
			})

			b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+tt.suffix))

			require.True(t, has(sent.edits, "🗂 "), "edits %q", sent.edits)
			require.True(t, has(sent.edits, "📄"), "edits %q", sent.edits)
			require.True(t, has(sent.editMarkups, "⬅️ Назад"), "markups %q", sent.editMarkups)
			require.True(t, has(sent.editMarkups, tt.wantBack), "markups %q", sent.editMarkups)
			require.Empty(t, sent.texts, "no file or message is sent")
		})
	}
}

func TestOpenCard_unknownDocumentIsGone(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:missing:s"))

	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
	require.True(t, has(sent.editMarkups, "Мои документы"), "markups %q", sent.editMarkups)
}

func TestSetCategory_editsCardInPlace(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":l:"+string(docsbot.CatPassport)+":1"))

	b.ProcessUpdate(ctx, dataCallback(100, "d:sc:"+string(doc.ID)+":"+string(docsbot.CatOther)))

	require.Len(t, sent.edits, 2)
	last := sent.edits[len(sent.edits)-1]
	require.Contains(t, last, "🗂 ")
	require.Contains(t, last, docsbot.CatOther.Label())
	require.True(t, has(sent.editMarkups, "d:lc:"+string(docsbot.CatPassport)+":1"), "origin kept: %q", sent.editMarkups)
	require.Empty(t, sent.texts, "no new card is sent")
}

func TestDeleteYes_returnsToOrigin(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seed := func(title, uid string) docsbot.Document {
		return repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
			Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
		})
	}
	first, second := seed("Первый", "u1"), seed("Второй", "u2")
	open := func(id docsbot.DocumentID) {
		b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(id)+":l:"+string(docsbot.CatPassport)+":1"))
	}

	open(first.ID)
	b.ProcessUpdate(ctx, dataCallback(100, "d:del:"+string(first.ID)))
	require.True(t, has(sent.edits, "Удалить этот документ"), "edits %q", sent.edits)
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:"+string(first.ID)))
	require.True(t, has(sent.edits, docsbot.CatPassport.Label()+" — 1"), "category page: %q", sent.edits)

	open(second.ID)
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:"+string(second.ID)))
	require.True(t, has(sent.edits, "Пока пусто"), "catalog when empty: %q", sent.edits)
	require.Empty(t, sent.texts, "everything is edited in place")
}

func TestDeleteYes_withoutOriginClearsKeyboard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})

	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)))
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:"+string(doc.ID)))

	require.True(t, has(sent.edits, "Документ удалён"), "edits %q", sent.edits)
	last := sent.editMarkups[len(sent.editMarkups)-1]
	require.NotEmpty(t, last, "reply_markup must be sent to clear the old keyboard")
	require.Contains(t, last, "Мои документы")
	require.Contains(t, last, `"d:lb"`)
	for _, cbData := range []string{"d:snd", "d:rn", "d:sc", "d:del", "d:oc"} {
		require.NotContains(t, last, cbData, "no card buttons remain")
	}
}

func TestSearchBack_rerunsLastSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	b.ProcessUpdate(ctx, textUpdate(100, "паспорт"))

	for range 2 { // the query survives repeated "Назад"
		b.ProcessUpdate(ctx, dataCallback(100, "d:sb"))
	}

	require.Len(t, sent.edits, 2)
	require.True(t, has(sent.edits, "Нашёл (1):"), "edits %q", sent.edits)
}

func TestLogs_traceDocumentWithoutLeaks(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	// Receive a file, choose a category, search, then ask for the file back.
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})
	require.Len(t, repo.docs, 1)
	var id docsbot.DocumentID
	for k := range repo.docs {
		id = k
	}
	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, textUpdate(100, "СекретныйЗапрос"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:snd:"+string(id)))

	out := buf.String()
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	require.Contains(t, out, "document received")
	require.Contains(t, out, "document saved")
	require.Contains(t, out, "document sent")
	require.Contains(t, out, `"op":"sendMessage"`)
	for _, leak := range []string{"Паспорт", "123:ABC", "СекретныйЗапрос", "chat_id", "tg_user_id", "f1"} {
		require.NotContains(t, out, leak)
	}
}

func TestLogs_editFlowWithoutLeaks(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	recv := func() {
		b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
			From:     &models.User{ID: 100},
			Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
			Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
		}})
	}
	recv()
	require.Len(t, repo.docs, 1)
	var id docsbot.DocumentID
	for k := range repo.docs {
		id = k
	}
	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:"+string(id)+":"+string(docsbot.CatOther)))
	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(id)))
	b.ProcessUpdate(ctx, textUpdate(100, "НовоеИмяСекрет"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:"+string(id)))

	out := buf.String()
	for _, ev := range []string{"document received", "document renamed", "document deleted"} {
		require.Contains(t, out, ev)
	}
	require.Contains(t, out, `"doc_id":"`+string(id)+`"`)
	for _, leak := range []string{"Паспорт", "НовоеИмяСекрет", "123:ABC", "chat_id", "tg_user_id", "f1", "u1"} {
		require.NotContains(t, out, leak)
	}
}

func seedPassport(repo *fakeRepo, title, uid string) docsbot.Document {
	return repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
	})
}

func TestSearchBack_withoutQueryFallsBackToCatalog(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, dataCallback(100, "d:sb"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Твои документы")
	require.Contains(t, sent.editMarkups[0], "d:lc:"+string(docsbot.CatPassport))
	require.Empty(t, sent.texts)
}

func TestDeleteYes_fromSearchReturnsToSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	keep := seedPassport(repo, "Паспорт один", "u1")
	gone := seedPassport(repo, "Паспорт два", "u2")
	b.ProcessUpdate(ctx, textUpdate(100, "паспорт"))

	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(gone.ID)+":s"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:"+string(gone.ID)))

	require.Equal(t, "Нашёл (1):", sent.edits[len(sent.edits)-1])
	require.NotContains(t, repo.docs, gone.ID)
	require.Contains(t, repo.docs, keep.ID)
}

func TestRename_editsCardInPlace(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":l:"+string(docsbot.CatPassport)+":1"))

	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))

	prompt := sent.edits[len(sent.edits)-1]
	require.Equal(t, "Пришли новое название", prompt, "the card message turns into the prompt")
	require.Equal(t, "d:rnc:"+string(doc.ID), cancelData(t, sent.editMarkups[len(sent.editMarkups)-1]))
	require.Empty(t, sent.texts, "no separate prompt message, no force reply")

	b.ProcessUpdate(ctx, textUpdateID(100, 55, "Новое имя"))

	require.Empty(t, sent.texts, "no new message at all")
	require.Equal(t, "Новое имя", repo.docs[doc.ID].Title)
	require.Equal(t, []int{55}, sent.deleteIDs, "the user's message is removed")
	require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1], "the same message is edited")
	require.Contains(t, sent.edits[len(sent.edits)-1], "Новое имя")
	require.True(t, has(sent.editMarkups, "d:lc:"+string(docsbot.CatPassport)+":1"), "origin kept: %q", sent.editMarkups)
}

// cancelData extracts the callback data of the only button of a prompt keyboard.
func cancelData(t *testing.T, markup string) string {
	t.Helper()
	var kb models.InlineKeyboardMarkup
	require.NoError(t, json.Unmarshal([]byte(markup), &kb))
	require.Len(t, kb.InlineKeyboard, 1)
	require.Len(t, kb.InlineKeyboard[0], 1)
	require.Equal(t, "Отмена", kb.InlineKeyboard[0][0].Text)
	return kb.InlineKeyboard[0][0].CallbackData
}

func TestRename_invalidTitleKeepsWaiting(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantHint string
	}{
		{"blank", " \n\u200b ", "Название пустое"},
		{"too long", strings.Repeat("я", docsbot.MaxTitleRunes+1), "слишком длинное"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := seedPassport(repo, "Старое", "u1")
			b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))

			b.ProcessUpdate(ctx, textUpdateID(100, 55, tt.text))

			require.Contains(t, sent.edits[len(sent.edits)-1], tt.wantHint, "hint in the same message")
			require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1])
			require.Equal(t, "d:rnc:"+string(doc.ID), cancelData(t, sent.editMarkups[len(sent.editMarkups)-1]))
			require.Empty(t, sent.texts)
			require.Equal(t, "Старое", repo.docs[doc.ID].Title)

			// The state is kept: the next valid name goes through.
			b.ProcessUpdate(ctx, textUpdateID(100, 56, "Хорошее имя"))
			require.Equal(t, "Хорошее имя", repo.docs[doc.ID].Title)
			require.Equal(t, []int{55, 56}, sent.deleteIDs)
		})
	}
}

func TestRename_cancelRestoresCardAndIsRepeatable(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":s"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))

	b.ProcessUpdate(ctx, dataCallback(100, "d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое", "card is back")
	require.True(t, has(sent.editMarkups[len(sent.editMarkups)-1:], "d:sb"), "with its Назад")

	// A repeated tap is harmless: it just redraws the card.
	b.ProcessUpdate(ctx, dataCallback(100, "d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")

	// The state is gone: a text is a search, not a rename.
	b.ProcessUpdate(ctx, textUpdateID(100, 60, "Новое"))
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
	require.Empty(t, sent.deleteIDs)
	require.True(t, has(sent.texts, "Ничего не нашёл") || has(sent.texts, "Нашёл"), "texts %q", sent.texts)
}

func TestRename_staleCancelHasNoSideEffects(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	other := seedPassport(repo, "Другое", "u2")
	// B is armed on message 8; a stale cancel of message 7 must not drop it.
	b.ProcessUpdate(ctx, dataCallbackMsg(100, 8, "d:rn:"+string(other.ID)))

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 7, "d:rnc:"+string(doc.ID)))

	require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1])
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")
	b.ProcessUpdate(ctx, textUpdateID(100, 61, "Имя для второго"))
	require.Equal(t, "Имя для второго", repo.docs[other.ID].Title, "the newer prompt is still armed")
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
}

func TestRename_otherButtonRestoresPromptCard(t *testing.T) {
	tests := []struct {
		name     string
		callback func(doc docsbot.Document) string
	}{
		{"open another card", func(docsbot.Document) string { return "d:lb" }},
		{"delete ask", func(d docsbot.Document) string { return "d:del:" + string(d.ID) }},
		{"category", func(d docsbot.Document) string { return "d:chc:" + string(d.ID) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := seedPassport(repo, "Старое", "u1")
			other := seedPassport(repo, "Другое", "u2")
			b.ProcessUpdate(ctx, dataCallbackMsg(100, 7, "d:rn:"+string(doc.ID)))
			sent.edits, sent.editIDs = nil, nil

			b.ProcessUpdate(ctx, dataCallbackMsg(100, 9, tt.callback(other)))

			require.Equal(t, 7, sent.editIDs[0], "the prompt message is returned to its card first")
			require.Contains(t, sent.edits[0], "Старое")
			// And the text is no longer a rename.
			b.ProcessUpdate(ctx, textUpdateID(100, 62, "Не имя"))
			require.Equal(t, "Старое", repo.docs[doc.ID].Title)
			require.Empty(t, sent.deleteIDs)
		})
	}
}

func TestRename_newPromptRestoresPreviousCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	a := seedPassport(repo, "Первое", "u1")
	c := seedPassport(repo, "Второе", "u2")
	b.ProcessUpdate(ctx, dataCallbackMsg(100, 7, "d:rn:"+string(a.ID)))
	sent.edits, sent.editIDs = nil, nil

	b.ProcessUpdate(ctx, dataCallbackMsg(100, 9, "d:rn:"+string(c.ID)))

	require.Equal(t, []int{7, 9}, sent.editIDs)
	require.Contains(t, sent.edits[0], "Первое", "the first message is a card again")
	require.Equal(t, "Пришли новое название", sent.edits[1])
	b.ProcessUpdate(ctx, textUpdateID(100, 63, "Новое для второго"))
	require.Equal(t, "Новое для второго", repo.docs[c.ID].Title)
	require.Equal(t, "Первое", repo.docs[a.ID].Title)
}

func TestRename_expiredStateIsASearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")

	// No armed rename (restart or TTL): the text goes to search, nothing is renamed or deleted.
	b.ProcessUpdate(ctx, textUpdateID(100, 64, "Новое имя"))

	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
	require.Empty(t, sent.deleteIDs)
	require.Len(t, sent.texts, 1)

	// A cancel on the stale prompt only restores the card.
	b.ProcessUpdate(ctx, dataCallback(100, "d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
}

func TestRename_deletedDocumentIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))
	delete(repo.docs, doc.ID)

	b.ProcessUpdate(ctx, textUpdateID(100, 65, "Новое"))

	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
}

func TestSend_sendsFileWithCaptionAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback(100, "d:snd:"+string(doc.ID)))
	require.Equal(t, []string{"Паспорт РК · " + docsbot.CatPassport.Label()}, sent.files)

	b.ProcessUpdate(ctx, dataCallback(100, "d:snd:missing"))
	require.True(t, has(sent.texts, "Этого документа уже нет"), "texts %q", sent.texts)
}

func TestDeleteNo_restoresCardAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback(100, "d:dn:"+string(doc.ID)))
	require.Contains(t, sent.edits[0], "🗂 ")

	b.ProcessUpdate(ctx, dataCallback(100, "d:dn:missing"))
	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
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

func TestSettingsActions_logEventsWithoutLeaks(t *testing.T) {
	var sink lockedBuffer
	log := slog.New(slog.NewJSONHandler(&sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, _, repo := newBotWithLog(t, log)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "ПаспортSECRET"})

	b.ProcessUpdate(ctx, dataCallback(100, "d:try"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:exp"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:dc"))
	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))

	out := sink.String()
	for _, ev := range []string{"transfer started", "export started", "drive disconnected", "delete data requested"} {
		require.Contains(t, out, ev)
	}
	for _, leak := range []string{"ПаспортSECRET", "SECRET", "123:ABC", "chat_id", "tg_user_id"} {
		require.NotContains(t, out, leak)
	}
}

func TestOpenCard_detectsFileDeletedInDrive(t *testing.T) {
	tests := []struct {
		name      string
		gone      bool
		wantText  string
		wantRetry bool
		wantOpen  bool
	}{
		{"file deleted", true, "Файл удалён в Drive, копия осталась в Telegram", true, false},
		{"file there", false, "Google Drive · копия в Telegram", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true, gone: tt.gone})
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{
				UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
				Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "remote", SyncStatus: docsbot.SyncOK,
			})

			b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":s"))

			require.True(t, has(sent.edits, tt.wantText), "edits %q", sent.edits)
			require.Equal(t, tt.wantRetry, has(sent.editMarkups, "d:rt:"+string(doc.ID)), "markups %q", sent.editMarkups)
			require.Equal(t, tt.wantOpen, has(sent.editMarkups, "drive.google.com/file"), "markups %q", sent.editMarkups)
		})
	}
}

// seedExport stores n saved documents of one kind, oldest first.
func seedExport(repo *fakeRepo, kind docsbot.Kind, n int, prefix string, base time.Time) {
	for i := range n {
		id := fmt.Sprintf("%s%d", prefix, i)
		repo.seed(docsbot.Document{
			UserID: testUserID, Status: docsbot.StatusSaved, Kind: kind, Category: docsbot.CatOther,
			Title: "Док " + id, TgFileID: "file-" + id, TgFileUniqueID: id, CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
}

func TestExport_sendsAlbumsByKind(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedExport(repo, docsbot.KindPhoto, 12, "p", base)                  // an album of 10, then an album of 2
	seedExport(repo, docsbot.KindDocument, 1, "d", base.Add(time.Hour)) // a lone document
	seedExport(repo, docsbot.KindPhoto, 1, "q", base.Add(2*time.Hour))  // a lone photo

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.Len(t, sent.groups, 2, "two albums")
	require.Equal(t, 10, strings.Count(sent.groups[0], `"type":"photo"`))
	require.Equal(t, 2, strings.Count(sent.groups[1], `"type":"photo"`))
	require.Contains(t, sent.groups[0], "Док p0 · Другое", "caption: title and category")
	require.Len(t, sent.files, 2, "the lone document and the lone photo are sent as files")
	require.True(t, has(sent.texts, "Присылаю"), "texts %q", sent.texts)
	require.True(t, has(sent.texts, "Готово — прислал 14"), "texts %q", sent.texts)
}

func TestExport_documentAlbum(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedExport(repo, docsbot.KindDocument, 3, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	b.ProcessUpdate(ctx, dataCallback(100, "d:exp"))

	require.Len(t, sent.groups, 1)
	require.Equal(t, 3, strings.Count(sent.groups[0], `"type":"document"`))
	require.Empty(t, sent.files)
}

func TestExport_empty(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.True(t, has(sent.texts, "нечего присылать"), "texts %q", sent.texts)
	require.Empty(t, sent.groups)
}

func TestExport_commandIsNotInTheMenu(t *testing.T) {
	for _, c := range telegram.Commands() {
		require.NotEqual(t, "export", c.Command)
	}
}

func TestDeleteData_promptOffersExportFirst(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "УДАЛИТЬ")
	require.Contains(t, sent.texts[0], "/export")
	require.Contains(t, sent.markups[0], "d:exp")
	require.Contains(t, sent.markups[0], "Сначала прислать все документы")
}

// Every card view is HTML (the title is escaped in cardText): sent, opened from a
// list, recategorized, and the rename prompt and hint around it.
func TestCard_isAlwaysSentAsHTML(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatPassport,
		Title: "A & <b>B</b>", TgFileID: "f", TgFileUniqueID: "u",
	})

	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":s"))
	require.Contains(t, sent.edits[len(sent.edits)-1], "<b>A &amp; &lt;b&gt;B&lt;/b&gt;</b>")
	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))
	b.ProcessUpdate(ctx, textUpdateID(100, 5, " "))
	b.ProcessUpdate(ctx, dataCallback(100, "d:rnc:"+string(doc.ID)))
	b.ProcessUpdate(ctx, dataCallback(100, "d:sc:"+string(doc.ID)+":"+string(docsbot.CatAuto)))
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From: &models.User{ID: 100}, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f", FileUniqueID: "u", FileName: "x.pdf"},
	}}) // the same file again: AlreadySaved card

	require.Len(t, sent.editModes, 5)
	for _, mode := range sent.editModes {
		require.Equal(t, "HTML", mode)
	}
	require.Equal(t, []string{"HTML"}, sent.sendModes, "the already-saved card")
}

func TestExport_cancelStopsBetweenBatches(t *testing.T) {
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(time.Hour, time.Hour))
	ctx, cancel := context.WithCancel(tg.ContextWithUserID(t.Context(), testUserID))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedExport(repo, docsbot.KindPhoto, 1, "p", base)
	seedExport(repo, docsbot.KindDocument, 1, "d", base.Add(time.Hour))
	time.AfterFunc(100*time.Millisecond, cancel)

	b.ProcessUpdate(ctx, commandUpdate(100, "export"))

	require.Len(t, sent.files, 1, "stops before the second batch")
	require.False(t, has(sent.texts, "Готово"), "texts %q", sent.texts)
}

func TestExport_exactlyTenDocumentsIsOneAlbumAndElevenSplits(t *testing.T) {
	for _, tt := range []struct {
		n          int
		wantGroups int
		wantFiles  int
	}{{10, 1, 0}, {11, 1, 1}} {
		b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithExportPace(0, 0))
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		seedExport(repo, docsbot.KindDocument, tt.n, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

		b.ProcessUpdate(ctx, commandUpdate(100, "export"))

		require.Len(t, sent.groups, tt.wantGroups, "n=%d", tt.n)
		require.Len(t, sent.files, tt.wantFiles, "n=%d", tt.n)
		require.True(t, has(sent.texts, fmt.Sprintf("прислал %d", tt.n)), "texts %q", sent.texts)
	}
}
