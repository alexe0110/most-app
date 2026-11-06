package telegram_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// capture records the last sendMessage the bot made to the fake Bot API.
type capture struct {
	chatID string
	text   string
}

// newBot stands up a fake Telegram Bot API, wires the docsbot handlers on a
// client pointed at it and returns the client plus the capture. Handlers run
// synchronously (WithNotAsyncHandlers) so a test can assert right after
// ProcessUpdate returns.
func newBot(t *testing.T) (*bot.Bot, *capture) {
	t.Helper()
	sent := &capture{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sent.chatID = r.FormValue("chat_id")
			sent.text = r.FormValue("text")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(ts.Close)

	b, err := bot.New("123:ABC",
		bot.WithServerURL(ts.URL),
		bot.WithSkipGetMe(),
		bot.WithNotAsyncHandlers(),
	)
	require.NoError(t, err)

	telegram.Register(b, slog.New(slog.DiscardHandler))
	return b, sent
}

func commandUpdate(chatID int64, cmd string) *models.Update {
	text := "/" + cmd
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: chatID},
			Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
			Text: text,
			Entities: []models.MessageEntity{
				{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len(text)},
			},
		},
	}
}

func textUpdate(chatID int64, text string) *models.Update {
	return &models.Update{
		Message: &models.Message{
			From: &models.User{ID: chatID},
			Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate},
			Text: text,
		},
	}
}

func TestRegister_routing(t *testing.T) {
	tests := []struct {
		name     string
		update   *models.Update
		wantText string // substring expected in the reply
	}{
		{"start command", commandUpdate(100, "start"), "Most Docs"},
		{"unknown command", commandUpdate(100, "foo"), "Не понимаю"},
		{"plain text", textUpdate(100, "привет"), "Не понимаю"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent := newBot(t)

			b.ProcessUpdate(t.Context(), tt.update)

			require.Equal(t, "100", sent.chatID)
			require.Contains(t, sent.text, tt.wantText)
		})
	}
}
