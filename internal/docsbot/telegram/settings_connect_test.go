package telegram_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
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
				b, _, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, telegram.WithSettingsMessages(msgs))
				b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), commandUpdate("settings"))
			}
			e := newNotifierEnv(t)
			notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
				telegram.WithConnectSettings(msgs, e.svc, docsbottest.FakeAuth{}))

			require.NoError(t, notify(t.Context(), docsbottest.TestUserID))

			sends := e.api.callsOf("sendMessage")
			require.Len(t, sends, 1)
			require.Equal(t, telegram.TextDriveConnected, sends[0].text)
			edits := e.api.callsOf("editMessageText")
			if !tt.wantEdit {
				require.Empty(t, edits)
				return
			}
			require.Len(t, edits, 1)
			require.Equal(t, "777", edits[0].messageID)
			require.Contains(t, edits[0].text, telegram.TextSettingsOn)
			require.Contains(t, edits[0].markup, telegram.BtnDisconnect)
		})
	}
}

func TestNotifyConnected_settingsFailuresDoNotFailTheCallback(t *testing.T) {
	remember := func(t *testing.T) *telegram.SettingsMessages {
		msgs := telegram.NewSettingsMessages()
		b, _, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, telegram.WithSettingsMessages(msgs))
		b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), commandUpdate("settings"))
		return msgs
	}

	t.Run("edit fails", func(t *testing.T) {
		msgs := remember(t)
		e := newNotifierEnv(t)
		e.api.editErr = "Bad Request: message to edit not found"
		notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
			telegram.WithConnectSettings(msgs, e.svc, docsbottest.FakeAuth{}))

		require.NoError(t, notify(t.Context(), docsbottest.TestUserID))
		require.Len(t, e.api.callsOf("editMessageText"), 1)
	})

	t.Run("settings cannot be loaded", func(t *testing.T) {
		msgs := remember(t)
		e := newNotifierEnv(t)
		notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler),
			telegram.WithConnectSettings(msgs, failingSettings{}, docsbottest.FakeAuth{}))

		require.NoError(t, notify(t.Context(), docsbottest.TestUserID))
		require.Empty(t, e.api.callsOf("editMessageText"))
	})
}

func TestNotifyConnected_transferOfferAgreesWithTheCount(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, ""},
		{1, "Загрузить на Google Диск 1 документ из Telegram?"},
		{2, "Загрузить на Google Диск 2 документа из Telegram?"},
		{5, "Загрузить на Google Диск 5 документов из Telegram?"},
		{11, "Загрузить на Google Диск 11 документов из Telegram?"},
		{21, "Загрузить на Google Диск 21 документ из Telegram?"},
		{22, "Загрузить на Google Диск 22 документа из Telegram?"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.n), func(t *testing.T) {
			e := newNotifierEnv(t)
			for i := range tt.n {
				e.repo.Seed(docsbot.Document{
					UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: fmt.Sprint("d", i), TgFileUniqueID: fmt.Sprint("u", i),
				})
			}
			notify := telegram.NotifyConnected(e.bot, chatLookup{}, e.svc, slog.New(slog.DiscardHandler))

			require.NoError(t, notify(t.Context(), docsbottest.TestUserID))

			sends := e.api.callsOf("sendMessage")
			if tt.want == "" {
				require.Len(t, sends, 1, "only the connected note when everything is already in Drive")
				return
			}
			require.Len(t, sends, 2)
			require.Equal(t, tt.want, sends[1].text)
			require.Contains(t, sends[1].markup, "d:try")
			require.Contains(t, sends[1].markup, "d:trn")
		})
	}
}
