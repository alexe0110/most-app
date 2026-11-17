package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// teamReply is a reply written in the feedback chat (a group) to message replyTo.
func teamReply(replyTo int, text string) *models.Update {
	return &models.Update{Message: &models.Message{
		ID: 9, From: &models.User{ID: 555}, Chat: models.Chat{ID: feedbackChat, Type: models.ChatTypeSupergroup},
		Text: text, ReplyToMessage: &models.Message{ID: replyTo},
	}}
}

// sentFeedback makes user 100 send one request; its header is message 777 in the
// feedback chat and its copy 901.
func sentFeedback(e *feedbackEnv) {
	e.send(commandUpdate("feedback"))
	e.send(userMessage(55, "привет"))
	e.sent.texts, e.sent.sendChats, e.sent.copies = nil, nil, nil
}

func TestFeedbackReply_goesToTheAuthor(t *testing.T) {
	for _, replyTo := range []int{sentMessageID, copiedMessageID} {
		t.Run("to the header or the copy", func(t *testing.T) {
			e := newFeedbackEnv(t, feedbackChat)
			sentFeedback(e)

			e.send(teamReply(replyTo, "Починили, попробуй ещё раз"))

			require.Equal(t, []string{"100", "-1001"}, e.sent.sendChats, "lead-in to the user, status to the team")
			require.Equal(t, []string{telegram.TextFeedbackAnswer, telegram.TextFeedbackDelivered}, e.texts())
			require.Equal(t, []string{"100<--1001:9"}, e.sent.copies, "the reply itself is copied to the user")
		})
	}
}

func TestFeedbackReply_blockedUser(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	sentFeedback(e)
	e.sent.blockedChat = "100"

	e.send(teamReply(sentMessageID, "ответ"))

	require.Equal(t, []string{telegram.TextFeedbackBlocked}, e.texts())
	require.Equal(t, []string{"-1001"}, e.sent.sendChats)
	require.Empty(t, e.sent.copies)
}

func TestFeedbackReply_otherFailure(t *testing.T) {
	e := newFeedbackEnv(t, feedbackChat)
	sentFeedback(e)
	e.sent.copyDown = true

	e.send(teamReply(sentMessageID, "ответ"))

	require.Equal(t, []string{telegram.TextFeedbackAnswer, telegram.TextFeedbackNotDelivered}, e.texts())
}

func TestFeedbackReply_unknownMessageIsNotAnAnswer(t *testing.T) {
	t.Run("in a group it is ignored", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		sentFeedback(e)

		e.send(teamReply(12345, "обсуждаем между собой"))

		require.Empty(t, e.sent.texts)
		require.Empty(t, e.sent.copies)
	})
	t.Run("in the owner's own chat it is handled as usual", func(t *testing.T) {
		e := newFeedbackEnv(t, 100) // the feedback chat is the owner's private chat
		seedPassport(e.repo, "Паспорт", "u1")
		u := userMessage(9, "паспорт")
		u.Message.ReplyToMessage = &models.Message{ID: 12345}

		e.send(u)

		require.True(t, has(e.texts(), "Нашёл"), "a search, not an answer: %q", e.texts())
		require.Empty(t, e.sent.copies)
	})
}

func TestFeedbackReply_onlyRepliesInTheFeedbackChatAreAnswers(t *testing.T) {
	tests := []struct {
		name   string
		update func() *models.Update
	}{
		{"a user's reply in their own chat", func() *models.Update {
			u := userMessage(9, "паспорт")
			u.Message.ReplyToMessage = &models.Message{ID: sentMessageID}
			return u
		}},
		{"a message that is no reply", func() *models.Update {
			u := teamReply(0, "паспорт")
			u.Message.ReplyToMessage = nil
			return u
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFeedbackEnv(t, feedbackChat)
			sentFeedback(e)

			e.send(tt.update())

			require.NotContains(t, e.texts(), telegram.TextFeedbackDelivered)
			require.NotContains(t, e.texts(), telegram.TextFeedbackAnswer)
			require.Empty(t, e.sent.copies, "nothing is delivered to the author")
		})
	}
}

func TestFeedbackReply_ownerChatStillWorksAsUsualChat(t *testing.T) {
	// The feedback chat is the owner's private chat with the bot.
	reply := func(u *models.Update) *models.Update {
		u.Message.ReplyToMessage = &models.Message{ID: 12345}
		return u
	}
	t.Run("a document replying to an unknown message is saved", func(t *testing.T) {
		e := newFeedbackEnv(t, 100)

		e.send(reply(userPhoto(9, "", "")))

		require.Empty(t, e.sent.copies)
		require.NotContains(t, e.texts(), telegram.TextFeedbackDelivered)
		require.NotEmpty(t, e.texts(), "the photo got the usual handling")
	})
	t.Run("a rename answer replying to the prompt is applied", func(t *testing.T) {
		e := newFeedbackEnv(t, 100)
		doc := seedPassport(e.repo, "Старое", "u1")
		e.send(dataCallback("d:rn:" + string(doc.ID)))

		e.send(reply(userMessage(63, "Новое имя")))

		require.Equal(t, "Новое имя", e.repo.Docs[doc.ID].Title)
		require.Empty(t, e.sent.copies)
	})
	t.Run("an answer to a known message still reaches the author", func(t *testing.T) {
		e := newFeedbackEnv(t, 100)
		e.repo.FeedbackTgUsers[docsbottest.TestUserID] = 555
		e.send(commandUpdate("feedback"))
		e.send(userMessage(55, "привет"))
		e.sent.sendChats, e.sent.copies = nil, nil

		u := userMessage(9, "ответ")
		u.Message.ReplyToMessage = &models.Message{ID: sentMessageID}
		e.send(u)

		require.Equal(t, []string{"555", "100"}, e.sent.sendChats)
	})
}

func TestFeedbackReply_withoutFeedbackChatNothingIsAnswered(t *testing.T) {
	e := newFeedbackEnv(t, 0)

	e.send(teamReply(sentMessageID, "ответ"))

	require.Empty(t, e.sent.copies)
	require.NotContains(t, e.texts(), telegram.TextFeedbackDelivered)
}
