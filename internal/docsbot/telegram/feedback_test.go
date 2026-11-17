package telegram_test

import (
	"context"
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

func TestFeedback_voiceIsRefusedAndModeStays(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	e.send(userVoice(70, ""))
	require.Equal(t, telegram.TextFeedbackUnsupported, e.texts()[1])
	require.Empty(t, e.sent.copies)

	e.send(userMessage(71, "тогда текстом"))
	require.Equal(t, []string{"-1001<-100:71"}, e.sent.copies, "the mode survived the refusal")
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

			require.Contains(t, e.texts(), tt.wantReply)
			require.Equal(t, tt.wantSent, len(e.sent.copies) == 1)
			require.Equal(t, tt.wantSent, has(e.texts(), telegram.TextFeedbackPrompt), "at the limit the user is not asked to write")
		})
	}
}

func TestFeedback_limitReachedWhileWriting(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	for range docsbot.MaxFeedbackPerHour {
		e.repo.SeedFeedback(docsbottest.TestUserID, time.Now())
	}
	e.send(userMessage(55, "ещё одно"))

	require.Equal(t, telegram.TextFeedbackLimit, e.texts()[len(e.texts())-1], "the check is repeated at send time")
	require.Empty(t, e.sent.copies)
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
		{"/start", func(e *feedbackEnv) { e.send(commandUpdate("start")) }},
		{"/export", func(e *feedbackEnv) { e.send(commandUpdate("export")) }},
		{"/convert", func(e *feedbackEnv) { e.send(commandUpdate("convert")) }},
		{"/cancel", func(e *feedbackEnv) { e.send(commandUpdate("cancel")) }},
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

func TestFeedback_unknownCommandKeepsTheMode(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)

	e.send(commandUpdate("feedback"))
	e.send(commandUpdate("nonsense"))
	e.send(userMessage(55, "всё-таки напишу"))

	require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
}

func TestFeedback_winsOverOtherPendingDialogues(t *testing.T) {
	t.Run("a live /convert session", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)

		e.send(commandUpdate("convert"))
		e.send(commandUpdate("feedback"))
		e.send(userPhoto(60, "", "скрин"))

		require.Equal(t, []string{"-1001<-100:60"}, e.sent.copies, "the photo is feedback, not a conversion input")
		require.Empty(t, e.sent.files)
		require.Empty(t, e.repo.Docs)
	})
	t.Run("an armed rename, by the command", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		doc := seedPassport(e.repo, "Старое", "u1")
		e.send(dataCallback("d:rn:" + string(doc.ID)))

		e.send(commandUpdate("feedback"))
		e.send(userMessage(55, "не работает"))
		e.send(userMessage(56, "паспорт"))

		require.Equal(t, "Старое", e.repo.Docs[doc.ID].Title, "neither the feedback nor the next text is a new title")
		require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
		require.True(t, has(e.texts(), "По запросу"), "the next text is a search: %q", e.texts())
	})
	t.Run("an armed delete confirmation", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		seedPassport(e.repo, "Паспорт", "u1")
		e.send(dataCallback("d:dd"))

		e.send(commandUpdate("feedback"))
		e.send(userMessage(55, "не работает"))
		e.send(userMessage(56, "УДАЛИТЬ"))

		require.Empty(t, e.repo.Deleted, "the word no longer confirms a deletion")
		require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
	})
	t.Run("an armed rename, by the settings button", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		doc := seedPassport(e.repo, "Старое", "u1")
		e.send(dataCallbackMsg(7, "d:rn:"+string(doc.ID)))

		e.send(dataCallbackMsg(8, "d:fb:s"))
		e.send(userMessage(55, "не работает"))
		e.send(userMessage(56, "паспорт"))

		require.Equal(t, "Старое", e.repo.Docs[doc.ID].Title, "the button ended the rename")
		require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
	})
}

func TestFeedback_logsHoldNoText(t *testing.T) {
	const secret = "СЕКРЕТНАЯ-ПОДПИСЬ"
	tests := []struct {
		name string
		do   func(ctx context.Context, b *bot.Bot)
	}{
		{"text", func(ctx context.Context, b *bot.Bot) { b.ProcessUpdate(ctx, userMessage(55, secret)) }},
		{"caption", func(ctx context.Context, b *bot.Bot) { b.ProcessUpdate(ctx, userPhoto(55, "", secret)) }},
		{"album", func(ctx context.Context, b *bot.Bot) {
			b.ProcessUpdate(ctx, userPhoto(55, "alb", secret))
			b.ProcessUpdate(ctx, userPhoto(56, "alb", secret))
		}},
		{"voice refused", func(ctx context.Context, b *bot.Bot) { b.ProcessUpdate(ctx, userVoice(55, "")) }},
		{"answer", func(ctx context.Context, b *bot.Bot) {
			b.ProcessUpdate(ctx, userMessage(55, secret))
			b.ProcessUpdate(ctx, teamReply(sentMessageID, secret))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs lockedBuffer
			ft := &docsbottest.FakeTimer{}
			b, sent, repo := newBotOpts(t, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				docsbottest.FakeDrive{}, telegram.WithFeedbackChat(feedbackChat), telegram.WithAlbumTimer(ft.After))
			repo.FeedbackTgUsers = map[docsbot.UserID]int64{docsbottest.TestUserID: 100}
			sent.copyDown = tt.name == "answer" // the delivery fails and is logged
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			b.ProcessUpdate(ctx, commandUpdate("feedback"))

			tt.do(ctx, b)
			ft.Fire()

			require.NotContains(t, logs.String(), secret)
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
	require.Empty(t, e.repo.Events, "a request that failed halfway is not a sent feedback")

	e.sent.copyDown = false
	e.send(commandUpdate("feedback"))
	e.send(userMessage(56, "ещё раз"))
	require.Len(t, e.repo.Events, 1, "the retry that went through is counted once")
	n, err := e.repo.CountFeedbackSince(t.Context(), docsbottest.TestUserID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, n, "the header left in the team's chat counts against the limit")
}

func TestFeedback_lostLinkDoesNotFailADeliveredRequest(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	e.repo.RecordFeedbackErr = errors.New("db down")

	e.send(commandUpdate("feedback"))
	e.send(userMessage(55, "привет"))

	require.Equal(t, telegram.TextFeedbackSent, e.texts()[len(e.texts())-1], "the team got it")
	require.Equal(t, []string{"-1001<-100:55"}, e.sent.copies)
	require.Equal(t, []docsbot.EventName{docsbot.EventFeedbackSent}, e.repo.EventNames())
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
