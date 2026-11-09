package telegram_test

import (
	"bytes"
	"errors"
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

	b.ProcessUpdate(ctx, dataCallback(100, "d:rt:"+string(doc.ID)))

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

			b.ProcessUpdate(ctx, dataCallback(100, "d:rt:"+string(doc.ID)))

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

	b.ProcessUpdate(ctx, dataCallback(100, "d:rt:"+string(doc.ID)))

	require.Equal(t, docsbot.BackupFailed, repo.docs[doc.ID].BackupStatus)
	require.Equal(t, []string{"Уже в работе или готово"}, sent.answers)
}

func TestSearch_blankQueryShowsHint(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "Паспорт", Category: docsbot.CatPassport})

	b.ProcessUpdate(ctx, textUpdate(100, "   "))

	require.True(t, has(sent.texts, "Напиши, что искать"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "Ничего не нашёл"))
}

func TestDeleteData_revokeFailureStillConfirms(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))
	b.ProcessUpdate(ctx, textUpdate(100, "УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
	require.Empty(t, repo.docs)
}

func TestDisconnect_driveFailureIsShown(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback(100, "d:dc"))

	require.False(t, has(sent.edits, "Google Drive отключён"), "the integration is still active: %q", sent.edits)
	require.True(t, has(sent.texts, "Что-то пошло не так"), "texts %q", sent.texts)
}

func TestSetCategory_backedUpCardLinksFileAndNotesOldFolder(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1", Title: "a",
	})

	b.ProcessUpdate(ctx, dataCallback(100, "d:sc:"+string(doc.ID)+":auto"))

	require.Equal(t, docsbot.CatAuto, repo.docs[doc.ID].Category)
	require.True(t, has(sent.edits, "В Drive файл остался в прежней папке"), "edits %q", sent.edits)
}

func TestOpenCard_backedUpShowsFileButNoFolder(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{connected: true})
	repo.folders = map[docsbot.Category]string{docsbot.CatAuto: "folder-1"}
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatAuto,
		BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1", Title: "a",
	})

	b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":s"))

	require.NotEmpty(t, sent.edits)
	require.False(t, has(sent.edits, "прежней папке"), "the note is one-shot")
}

func TestRetry_earlyExitsStillAnswerCallback(t *testing.T) {
	t.Run("no user in context", func(t *testing.T) {
		b, sent, _ := newBot(t)

		b.ProcessUpdate(t.Context(), dataCallback(100, "d:rt:doc-1"))

		require.Equal(t, []string{""}, sent.answers)
	})
	t.Run("no document id", func(t *testing.T) {
		b, sent, _ := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)

		b.ProcessUpdate(ctx, dataCallback(100, "d:rt"))

		require.Equal(t, []string{""}, sent.answers)
	})
}

func TestDeleteData_revokeFailureIsWarnedAndConfirmed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, sent, _ := newBotWith(t, log, fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback(100, "d:dd"))
	b.ProcessUpdate(ctx, textUpdate(100, "УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Contains(t, buf.String(), `"level":"WARN"`)
	require.Contains(t, buf.String(), "drive revoke incomplete")
	require.NotContains(t, buf.String(), "delete all data", "not treated as a failure")
}

func TestCategory_missingDocumentSaysGone(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback(100, "d:cat:missing:"+string(docsbot.CatOther)))

	require.Contains(t, sent.edits[len(sent.edits)-1], "Этого документа уже нет")
}
