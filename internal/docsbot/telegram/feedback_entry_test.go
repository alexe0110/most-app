package telegram_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

func TestFeedback_unavailableWithoutChat(t *testing.T) {
	tests := []struct {
		name string
		do   func(*feedbackEnv)
	}{
		{"command", func(e *feedbackEnv) { e.send(commandUpdate("feedback")) }},
		{"settings button", func(e *feedbackEnv) { e.send(dataCallback("d:fb:s")) }},
		{"error button", func(e *feedbackEnv) { e.send(dataCallback("d:fb:e:settings:1795000000")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFeedbackEnv(t, 0)
			seedPassport(e.repo, "Паспорт", "u1")

			tt.do(e)

			require.Equal(t, []string{telegram.TextFeedbackUnavailable}, e.texts())
			e.send(userMessage(55, "паспорт"))
			require.True(t, has(e.texts(), "Нашёл"), "the rest of the bot works: %q", e.texts())
			require.Empty(t, e.sent.copies)
			require.Empty(t, e.repo.Feedbacks)
		})
	}
}

func TestFeedback_entryPoints(t *testing.T) {
	t.Run("settings has the button", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)

		e.send(commandUpdate("settings"))

		require.Contains(t, e.sent.markups[0], telegram.BtnFeedback)
		require.Contains(t, e.sent.markups[0], "d:fb:s")
	})
	t.Run("an error carries the report button", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		e.repo.InlineErr = errors.New("db down")

		e.send(commandUpdate("settings"))

		require.Equal(t, telegram.TextError, e.texts()[0], "the text is unchanged")
		require.Contains(t, e.sent.markups[0], telegram.BtnReportProblem)
		require.Contains(t, e.sent.markups[0], "d:fb:e:settings:")
	})
	t.Run("a file that cannot be sent carries the report button", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		doc := seedPassport(e.repo, "Паспорт", "u1")
		e.sent.filesDown = true

		e.send(dataCallback("d:snd:" + string(doc.ID)))

		require.Equal(t, telegram.TextSendFailed, e.texts()[0])
		require.Contains(t, e.sent.markups[0], "d:fb:e:send_file:")
	})
	t.Run("an album that cannot be sent carries the report button", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		e.sent.mediaDown = true
		seedExport(e.repo, docsbot.KindDocument, 3, "d", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

		e.send(commandUpdate("export"))

		require.Contains(t, strings.Join(e.sent.markups, "\n"), "d:fb:e:export_send:")
	})
	t.Run("help and the menu name the command", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)

		e.send(commandUpdate("help"))

		require.Contains(t, e.texts()[0], "/feedback")
		var found bool
		for _, c := range telegram.Commands() {
			found = found || c.Command == "feedback"
		}
		require.True(t, found, "setMyCommands lists /feedback")
	})
}

func TestFeedback_sourceIsNamedInTheHeader(t *testing.T) {
	tests := []struct {
		name   string
		button string
		want   string
		source string
	}{
		{"settings", "d:fb:s", "Источник: настройки", "settings"},
		{"error", "d:fb:e:load_card:1795000000", "Источник: ошибка: load_card, 2026-11-18 11:06:40 UTC\n", "error"},
		{"error without time", "d:fb:e:load_card", "Источник: ошибка: load_card\n", "error"},
		{"odd scenario is not shown", "d:fb:e:Bad%20Text:x", "Источник: ошибка: —\n", "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFeedbackEnv(t, feedbackChat)

			e.send(dataCallback(tt.button))
			require.Equal(t, telegram.TextFeedbackPrompt, e.texts()[0])
			e.send(userMessage(55, "не работает"))

			require.Contains(t, e.texts()[1], tt.want)
			require.Equal(t, tt.source, e.repo.Events[0].Props.Source)
		})
	}
}
