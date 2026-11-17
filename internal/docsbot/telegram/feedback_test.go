package telegram_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

const feedbackChat = int64(-1001)

// feedbackEnv is a bot with the feedback chat set, driven as user 100.
type feedbackEnv struct {
	bot   *bot.Bot
	sent  *capture
	repo  *docsbottest.FakeRepo
	timer *docsbottest.FakeTimer
	send  func(*models.Update)
}

func newFeedbackEnv(t *testing.T, chat int64) *feedbackEnv {
	t.Helper()
	ft := &docsbottest.FakeTimer{}
	b, sent, repo := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{},
		telegram.WithFeedbackChat(chat), telegram.WithAlbumTimer(ft.After))
	repo.FeedbackTgUsers = map[docsbot.UserID]int64{docsbottest.TestUserID: 100}
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	return &feedbackEnv{bot: b, sent: sent, repo: repo, timer: ft, send: func(u *models.Update) { b.ProcessUpdate(ctx, u) }}
}

func userMessage(id int, text string) *models.Update {
	u := textUpdateID(id, text)
	u.Message.From.Username = "ivan_k"
	u.Message.From.LanguageCode = "ru"
	return u
}

func userPhoto(id int, group, caption string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: id, From: &models.User{ID: 100}, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: group, Caption: caption,
		Photo: []models.PhotoSize{{FileID: "f", FileUniqueID: "u", FileSize: 1000}},
	}}
}

func userVoice(id int, group string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: id, From: &models.User{ID: 100}, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		MediaGroupID: group, Voice: &models.Voice{FileID: "v"},
	}}
}

func (e *feedbackEnv) texts() []string { return e.sent.snapshot(&e.sent.texts) }

func TestFeedback_commandThenTextIsCopiedAndNotSaved(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	require.Equal(t, []string{telegram.TextFeedbackPrompt}, e.texts())
	require.Contains(t, e.sent.markups[0], telegram.BtnCancel)
	require.Contains(t, e.sent.markups[0], "d:fbx")

	e.send(userMessage(55, "паспорт не открывается"))

	texts := e.texts()
	require.Len(t, texts, 3, "prompt, header, thanks: %q", texts)
	require.Equal(t, []string{"100", "-1001", "100"}, e.sent.sendChats)
	require.Contains(t, texts[1], "Telegram id: 100")
	require.Contains(t, texts[1], "@ivan_k")
	require.Contains(t, texts[1], "Язык: ru")
	require.Contains(t, texts[1], "Источник: команда /feedback")
	require.Equal(t, telegram.TextFeedbackSent, texts[2])
	require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
	require.Empty(t, e.repo.Docs, "feedback is not a document")
	require.NotContains(t, strings.Join(texts, "\n"), "Нашёл", "feedback is not a search")
	require.Len(t, e.sent.markupEdits, 1, "the prompt loses its cancel button")

	require.Len(t, e.repo.Feedbacks, 1)
	require.Equal(t, docsbot.FeedbackRecord{User: docsbottest.TestUserID, ChatID: feedbackChat, MessageIDs: []int{sentMessageID, copiedMessageID}},
		e.repo.Feedbacks[0].Record, "the header and the copy lead back to the user")
	require.Equal(t, []docsbot.Event{{
		User: docsbottest.TestUserID, Name: docsbot.EventFeedbackSent, Props: docsbot.EventProps{Source: "command"},
	}}, e.repo.Events)
}

func TestFeedback_modeEndsAfterOneMessage(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	seedPassport(e.repo, "Паспорт", "u1")

	e.send(commandUpdate("feedback"))
	e.send(userMessage(55, "привет"))
	e.send(userMessage(56, "паспорт"))

	require.Len(t, e.sent.copies, 1, "the second message is not feedback")
	require.True(t, has(e.texts(), "Нашёл"), "it is a search: %q", e.texts())
}

func TestFeedback_photoWithCaptionIsCopiedNotSaved(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	e.send(userPhoto(60, "", "вот скриншот"))

	require.Equal(t, []string{"-1001<-100:60"}, e.sent.copies)
	require.Empty(t, e.repo.Docs, "the photo is not saved")
	require.False(t, has(e.texts(), "категор"), "no category picker: %q", e.texts())
	require.Len(t, e.repo.Feedbacks, 1)
}

func TestFeedback_albumIsOneRequest(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	for _, id := range []int{63, 61, 62} {
		e.send(userPhoto(id, "alb", ""))
	}
	require.Empty(t, e.sent.copies, "nothing is sent before the parts stop coming")
	e.timer.Fire()

	require.Equal(t, []string{"-1001<-100:[61,62,63]"}, e.sent.copies, "one copy call, ids ascending")
	require.Len(t, e.repo.Feedbacks, 1, "one request")
	require.Equal(t, []int{sentMessageID, copiedMessageID, copiedMessageID + 1, copiedMessageID + 2}, e.repo.Feedbacks[0].Record.MessageIDs)
	require.Len(t, e.repo.Events, 1, "one event")
	require.Empty(t, e.repo.Docs, "no photo of the album is saved")
	require.Equal(t, 1, strings.Count(strings.Join(e.texts(), "\n"), telegram.TextFeedbackSent), "one thanks")
}

func TestFeedback_voiceIsRefusedAndModeStays(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	e.send(userVoice(70, ""))
	require.Equal(t, telegram.TextFeedbackUnsupported, e.texts()[1])
	require.Empty(t, e.sent.copies)

	e.send(userMessage(71, "тогда текстом"))
	require.Equal(t, []string{"-1001<-100:71"}, e.sent.copies, "the mode survived the refusal")
}

func TestFeedback_albumWithUnsupportedPartIsRefusedWhole(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	e.send(userPhoto(80, "alb", ""))
	e.send(userVoice(81, "alb"))
	e.timer.Fire()

	require.Equal(t, 1, strings.Count(strings.Join(e.texts(), "\n"), telegram.TextFeedbackUnsupported), "refused once")
	require.Empty(t, e.sent.copies)
	require.Empty(t, e.repo.Feedbacks)

	e.send(userMessage(82, "ладно, текстом"))
	require.Equal(t, []string{"-1001<-100:82"}, e.sent.copies, "the mode survived the refusal")
}

func TestFeedback_limit(t *testing.T) {
	tests := []struct {
		name      string
		before    int
		wantSent  bool
		wantReply string
	}{
		{"fifth request goes", docsbot.MaxFeedbackPerHour - 1, true, telegram.TextFeedbackSent},
		{"sixth request is refused", docsbot.MaxFeedbackPerHour, false, telegram.TextFeedbackLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFeedbackEnv(t, feedbackChat)
			for range tt.before {
				e.repo.SeedFeedback(docsbottest.TestUserID, time.Now())
			}

			e.send(commandUpdate("feedback"))
			e.send(userMessage(55, "ещё одно"))

			require.Equal(t, tt.wantReply, e.texts()[len(e.texts())-1])
			require.Equal(t, tt.wantSent, len(e.sent.copies) == 1)
		})
	}
}

func TestFeedback_cancel(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	seedPassport(e.repo, "Паспорт", "u1")

	e.send(commandUpdate("feedback"))
	e.send(dataCallback("d:fbx"))
	require.Equal(t, []string{telegram.TextFeedbackCanceled}, e.sent.edits)

	e.send(userMessage(55, "паспорт"))
	require.Empty(t, e.sent.copies, "nothing is sent after the cancel")
	require.True(t, has(e.texts(), "Нашёл"), "an ordinary search: %q", e.texts())
}

func TestFeedback_otherCommandsAndButtonsEndTheMode(t *testing.T) {
	tests := []struct {
		name string
		do   func(*feedbackEnv)
	}{
		{"/list", func(e *feedbackEnv) { e.send(commandUpdate("list")) }},
		{"/settings", func(e *feedbackEnv) { e.send(commandUpdate("settings")) }},
		{"/help", func(e *feedbackEnv) { e.send(commandUpdate("help")) }},
		{"/convert", func(e *feedbackEnv) { e.send(commandUpdate("convert")) }},
		{"a docs button", func(e *feedbackEnv) { e.send(dataCallback("d:lb")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFeedbackEnv(t, feedbackChat)
			e.send(commandUpdate("feedback"))

			tt.do(e)
			e.send(userMessage(55, "что-то"))

			require.Empty(t, e.sent.copies, "the message is not feedback any more")
		})
	}
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

func TestFeedback_copyFailureTellsTheUserAndKeepsTheHeader(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	e.sent.copyDown = true

	e.send(commandUpdate("feedback"))
	e.send(userMessage(55, "привет"))

	require.Equal(t, telegram.TextFeedbackFailed, e.texts()[len(e.texts())-1])
	require.Len(t, e.repo.Feedbacks, 1, "the header is in the chat, a reply to it must find the user")
	require.Equal(t, []int{sentMessageID}, e.repo.Feedbacks[0].Record.MessageIDs)
}

func TestFeedback_textIsNeverLogged(t *testing.T) {
	var logs lockedBuffer
	b, _, repo := newBotOpts(t, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), docsbottest.FakeDrive{},
		telegram.WithFeedbackChat(feedbackChat))
	repo.FeedbackErr = errors.New("db down")
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, commandUpdate("feedback"))
	b.ProcessUpdate(ctx, userMessage(55, "СЕКРЕТНЫЙ ТЕКСТ обращения"))

	require.NotContains(t, logs.String(), "СЕКРЕТНЫЙ")
	require.Contains(t, logs.String(), "send feedback", "the failure itself is logged")
}

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

			tt.do(e)

			require.Equal(t, []string{telegram.TextFeedbackUnavailable}, e.texts())
			e.send(userMessage(55, "привет"))
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
