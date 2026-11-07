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
	mu    sync.Mutex
	texts []string // sendMessage
	edits []string // editMessageText
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
