package telegram_test

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestDisconnect_driveFailureIsShown(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.False(t, has(sent.edits, "Google Диск отключён"), "the integration is still active: %q", sent.edits)
	require.True(t, has(sent.texts, "Что-то пошло не так"), "texts %q", sent.texts)
}

func TestDisconnect_clearsKeyboardWithEmptyArray(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.True(t, has(sent.edits, "Google Диск отключён"), "edits %q", sent.edits)
	markups := sent.snapshot(&sent.editMarkups)
	require.NotEmpty(t, markups)
	last := markups[len(markups)-1]
	require.Contains(t, last, `"inline_keyboard":[]`, "an empty array clears the keyboard")
	require.NotContains(t, last, `"inline_keyboard":null`, "Telegram rejects a null inline_keyboard")
}

func TestDisconnect_confirmation(t *testing.T) {
	tests := []struct {
		name             string
		taps             []string
		wantDisconnected bool
		wantLastEdit     string // substring of the last edit
		wantLastMarkup   string // substring of the markup of the last edit
	}{
		{"ask shows the question with both answers", []string{"d:dca"}, false, "Отключить Google Диск? Файлы, которые уже там, останутся.", "d:dcn"},
		{"ask offers the confirming button", []string{"d:dca"}, false, "Новые документы будут храниться в Telegram.", `"callback_data":"d:dc"`},
		{"yes disconnects", []string{"d:dca", "d:dc"}, true, "Google Диск отключён", `"inline_keyboard":[]`},
		{"cancel brings the settings back and keeps the connection", []string{"d:dca", "d:dcn"}, false, "Google Диск: подключён ✅", "d:dca"},
		{"cancel twice is harmless", []string{"d:dca", "d:dcn", "d:dcn"}, false, "Google Диск: подключён ✅", "d:dca"},
		{"cancel on a stale button just redraws the settings", []string{"d:dcn"}, false, "Google Диск: подключён ✅", "d:dca"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var disconnected bool
			b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, Disconnected: &disconnected})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			for _, data := range tt.taps {
				b.ProcessUpdate(ctx, dataCallback(data))
			}

			require.Equal(t, tt.wantDisconnected, disconnected)
			require.Contains(t, sent.edits[len(sent.edits)-1], tt.wantLastEdit)
			require.Contains(t, sent.editMarkups[len(sent.editMarkups)-1], tt.wantLastMarkup)
			require.Empty(t, sent.texts, "the settings message is edited in place")
		})
	}
}

func TestDisconnect_confirmationNeedsNoMessage(t *testing.T) {
	for _, data := range []string{"d:dca", "d:dcn"} {
		t.Run(data, func(t *testing.T) {
			var disconnected bool
			b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{IsConnected: true, Disconnected: &disconnected})
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			b.ProcessUpdate(ctx, inaccessible(dataCallback(data)))

			require.False(t, disconnected)
			require.Empty(t, sent.edits)
			require.Empty(t, sent.texts)
		})
	}
}
