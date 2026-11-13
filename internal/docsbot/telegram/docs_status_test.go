package telegram_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestRetry_failedDocumentIsRequeued(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})

	b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

	require.Equal(t, docsbot.BackupPending, repo.docs[doc.ID].BackupStatus)
	require.True(t, has(sent.edits, "Повторяю выгрузку"), "edits %q", sent.edits)
	require.Equal(t, []string{""}, sent.answers, "no toast for a successful retry")
}

func TestRetry_notFailedShowsQuietToast(t *testing.T) {
	for _, st := range []docsbot.BackupStatus{docsbot.BackupBackedUp, docsbot.BackupUploading, docsbot.BackupPending} {
		t.Run(string(st), func(t *testing.T) {
			b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: st})

			b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

			require.Equal(t, []string{"Уже в работе или готово"}, sent.answers)
			require.Empty(t, sent.edits, "the card must not be rewritten")
			require.Empty(t, sent.texts, "and no extra message is sent")
			require.Equal(t, st, repo.docs[doc.ID].BackupStatus)
		})
	}
}

func TestRetry_foreignDocumentIsIgnored(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), "99999999-9999-9999-9999-999999999999")
	doc := repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, BackupStatus: docsbot.BackupFailed})

	b.ProcessUpdate(ctx, dataCallback("d:rt:"+string(doc.ID)))

	require.Equal(t, docsbot.BackupFailed, repo.docs[doc.ID].BackupStatus)
	require.Equal(t, []string{"Уже в работе или готово"}, sent.answers)
}

func TestRetry_earlyExitsStillAnswerCallback(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		b, sent, _ := newBot(t)

		b.ProcessUpdate(t.Context(), dataCallback("d:rt:doc-1"))

		require.Equal(t, []string{""}, sent.answers)
	})
	t.Run("no document id", func(t *testing.T) {
		b, sent, _ := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)

		b.ProcessUpdate(ctx, dataCallback("d:rt"))

		require.Equal(t, []string{""}, sent.answers)
	})
}
