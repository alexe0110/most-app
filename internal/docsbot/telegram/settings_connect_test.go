package telegram_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

type failingSettings struct{}

func (failingSettings) Settings(context.Context, docsbot.UserID) (docsbot.SettingsView, error) {
	return docsbot.SettingsView{}, errors.New("boom")
}

// After OAuth the /settings message the user came from becomes the connected
// screen; a message the bot does not remember (restart, expired) is left alone.
func TestNotifyConnected_settingsMessage(t *testing.T) {
	tests := []struct {
		name     string
		openFrom bool // the user opened /settings before connecting
		wantEdit bool
	}{
		{"known settings message is edited", true, true},
		{"unknown settings message is skipped silently", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := telegram.NewSettingsMessages()
			if tt.openFrom {
				b, _, _ := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithSettingsMessages(msgs))
				b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), commandUpdate(100, "settings"))
			}
			e := newNotifierEnv(t)
			notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
				telegram.WithConnectSettings(msgs, e.svc, fakeAuth{}))

			require.NoError(t, notify(t.Context(), testUserID))

			sends := e.api.callsOf("sendMessage")
			require.Len(t, sends, 1)
			require.Equal(t, "Google Диск подключён ✅", sends[0].text)
			edits := e.api.callsOf("editMessageText")
			if !tt.wantEdit {
				require.Empty(t, edits)
				return
			}
			require.Len(t, edits, 1)
			require.Equal(t, "777", edits[0].messageID)
			require.Contains(t, edits[0].text, "Google Диск: подключён ✅")
			require.Contains(t, edits[0].markup, "Отключить Google Диск")
		})
	}
}

func TestNotifyConnected_settingsFailuresDoNotFailTheCallback(t *testing.T) {
	remember := func(t *testing.T) *telegram.SettingsMessages {
		msgs := telegram.NewSettingsMessages()
		b, _, _ := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithSettingsMessages(msgs))
		b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), commandUpdate(100, "settings"))
		return msgs
	}

	t.Run("edit fails", func(t *testing.T) {
		msgs := remember(t)
		e := newNotifierEnv(t)
		e.api.editErr = "Bad Request: message to edit not found"
		notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
			telegram.WithConnectSettings(msgs, e.svc, fakeAuth{}))

		require.NoError(t, notify(t.Context(), testUserID))
		require.Len(t, e.api.callsOf("editMessageText"), 1)
	})

	t.Run("settings cannot be loaded", func(t *testing.T) {
		msgs := remember(t)
		e := newNotifierEnv(t)
		notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
			telegram.WithConnectSettings(msgs, failingSettings{}, fakeAuth{}))

		require.NoError(t, notify(t.Context(), testUserID))
		require.Empty(t, e.api.callsOf("editMessageText"))
	})
}
