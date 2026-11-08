package telegram_test

import (
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
	mu      sync.Mutex
	texts   []string // sendMessage
	edits   []string // editMessageText
	markups []string // sendMessage reply_markup
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
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sent.record(&sent.texts, r.FormValue("text"))
			sent.record(&sent.markups, r.FormValue("reply_markup"))
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			sent.record(&sent.edits, r.FormValue("text"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers())
	require.NoError(t, err)

	svc, auth, repo := fakeDeps()
	telegram.Register(b, slog.New(slog.DiscardHandler), svc, auth)
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

	require.True(t, has(sent.texts, "Сохранил"), "sent %q", sent.texts)
	require.Equal(t, docsbot.StatusSaved, repo.docs[doc.ID].Status)
	require.Equal(t, docsbot.CatOther, repo.docs[doc.ID].Category)
}

func dataCallback(chatID int64, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   "cb",
		From: models.User{ID: chatID},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}},
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
