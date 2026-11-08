package telegram_test

import (
	"bytes"
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
}

func (c *capture) record(bucket *[]string, s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*bucket = append(*bucket, s)
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
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent.record(&sent.texts, r.FormValue("text"))
			sent.record(&sent.markups, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/sendDocument"), strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			sent.record(&sent.files, r.FormValue("caption"))
		case strings.HasSuffix(r.URL.Path, "/deleteMessage"):
			sent.mu.Lock()
			sent.deletes++
			sent.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
			return
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			sent.record(&sent.edits, r.FormValue("text"))
			sent.record(&sent.editMarkups, r.FormValue("reply_markup"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)

	svc, auth, repo := fakeDepsWith(drive)
	telegram.Register(b, log, svc, auth)
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
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "drive-1",
	})
	b.ProcessUpdate(ctx, &models.Update{Message: &models.Message{
		From:     &models.User{ID: 100},
		Chat:     models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: "f1", FileUniqueID: "u1", FileName: "Паспорт.pdf"},
	}})

	require.True(t, has(sent.texts, "Паспорт"), "sent %q", sent.texts)
	require.True(t, has(sent.markups, "https://drive.google.com/drive/folders/fld-1"), "markups %q", sent.markups)
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
	require.True(t, has(sent.edits, "Категория:"), "edits %q", sent.edits)
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

			require.True(t, has(sent.edits, "Категория:"), "edits %q", sent.edits)
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
	require.Contains(t, last, "Категория:")
	require.Contains(t, last, telegram.CategoryLabel(docsbot.CatOther))
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
	require.True(t, has(sent.edits, telegram.CategoryLabel(docsbot.CatPassport)+" — 1"), "category page: %q", sent.edits)

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
	require.Len(t, sent.texts, 1, "only the force-reply prompt is sent")

	b.ProcessUpdate(ctx, textUpdate(100, "Новое имя"))

	require.Len(t, sent.texts, 1, "no new card message")
	require.Equal(t, "Новое имя", repo.docs[doc.ID].Title)
	require.Contains(t, sent.edits[len(sent.edits)-1], "Новое имя")
	require.True(t, has(sent.editMarkups, "d:lc:"+string(docsbot.CatPassport)+":1"), "origin kept: %q", sent.editMarkups)
}

func TestRename_deletedDocumentIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback(100, "d:rn:"+string(doc.ID)))
	delete(repo.docs, doc.ID)

	b.ProcessUpdate(ctx, textUpdate(100, "Новое"))

	require.True(t, has(sent.texts, "Этого документа уже нет"), "texts %q", sent.texts)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
}

func TestSend_sendsFileWithCaptionAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback(100, "d:snd:"+string(doc.ID)))
	require.Equal(t, []string{"Паспорт РК · " + telegram.CategoryLabel(docsbot.CatPassport)}, sent.files)

	b.ProcessUpdate(ctx, dataCallback(100, "d:snd:missing"))
	require.True(t, has(sent.texts, "Этого документа уже нет"), "texts %q", sent.texts)
}

func TestDeleteNo_restoresCardAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback(100, "d:dn:"+string(doc.ID)))
	require.Contains(t, sent.edits[0], "Категория:")

	b.ProcessUpdate(ctx, dataCallback(100, "d:dn:missing"))
	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
}
