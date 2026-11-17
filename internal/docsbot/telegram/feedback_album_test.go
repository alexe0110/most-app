package telegram_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

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

func TestFeedback_albumParts(t *testing.T) {
	t.Run("parts handled concurrently are one request", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		e.send(commandUpdate("feedback"))

		var wg sync.WaitGroup
		for _, id := range []int{64, 61, 63, 62, 65} {
			wg.Add(1)
			go func() { defer wg.Done(); e.send(userPhoto(id, "alb", "")) }()
		}
		wg.Wait()
		e.timer.Fire()

		require.Equal(t, []string{"-1001<-100:[61,62,63,64,65]"}, e.sent.copies)
		require.Len(t, e.repo.Feedbacks, 1)
		require.Len(t, e.repo.Events, 1)
		require.Empty(t, e.repo.Docs)
	})
	t.Run("a late part is neither saved nor searched", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		e.send(commandUpdate("feedback"))
		e.send(userPhoto(61, "alb", ""))
		e.timer.Fire()

		e.send(userPhoto(62, "alb", ""))
		e.timer.Fire()

		require.Equal(t, []string{"-1001<-100:61", "-1001<-100:62"}, e.sent.copies, "it reaches the team as its own request")
		require.Empty(t, e.repo.Docs)
	})
	t.Run("a second album needs a new request", func(t *testing.T) {
		e := newFeedbackEnv(t, feedbackChat)
		e.send(commandUpdate("feedback"))
		e.send(userPhoto(61, "one", ""))
		e.timer.Fire()

		e.send(userPhoto(70, "two", ""))
		e.timer.Fire()

		require.Equal(t, []string{"-1001<-100:61"}, e.sent.copies, "the mode ended with the first album")
		require.NotEmpty(t, e.repo.Docs, "the second album is an ordinary upload")
	})
}
