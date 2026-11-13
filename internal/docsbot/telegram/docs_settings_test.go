package telegram_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestDeleteData_revokeFailureStillConfirms(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
	require.Empty(t, repo.docs)
}

func TestDisconnect_driveFailureIsShown(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.False(t, has(sent.edits, "Google Диск отключён"), "the integration is still active: %q", sent.edits)
	require.True(t, has(sent.texts, "Что-то пошло не так"), "texts %q", sent.texts)
}

func TestDeleteData_revokeFailureIsWarnedAndConfirmed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, sent, _ := newBotWith(t, log, fakeDrive{revokeErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Contains(t, buf.String(), `"level":"WARN"`)
	require.Contains(t, buf.String(), "drive revoke incomplete")
	require.NotContains(t, buf.String(), "delete all data", "not treated as a failure")
}

func TestStart_driveFolder(t *testing.T) {
	b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), fakeDrive{}, telegram.WithDriveFolder("Most Docs Test"))

	b.ProcessUpdate(t.Context(), commandUpdate(100, "start"))

	require.True(t, has(sent.texts, "«Most Docs Test»"), "sent %q", sent.texts)
}

func TestSettings_showsCountsAndControls(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "p", TgFileUniqueID: "u1"})
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "o", TgFileUniqueID: "u2"})

	b.ProcessUpdate(ctx, commandUpdate(100, "settings"))

	require.True(t, has(sent.texts, "Google Диск"), "sent %q", sent.texts)
	require.True(t, has(sent.texts, "Документов: 2"), "sent %q", sent.texts)
}

func TestDeleteData_confirmation(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	// Tapping "Удалить мои данные" asks for the exact word.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	require.True(t, has(sent.texts, "УДАЛИТЬ"), "prompt asks for the word: %q", sent.texts)

	// A wrong word cancels and leaves the data in place.
	b.ProcessUpdate(ctx, textUpdate("удалить"))
	require.True(t, has(sent.texts, "отменено"), "sent %q", sent.texts)
	require.Len(t, repo.docs, 1, "data intact after a wrong word")

	// Re-arm, then the exact word deletes everything.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Empty(t, repo.docs, "data deleted after the exact word")
}

func TestDeleteData_promptOffersExportFirst(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "УДАЛИТЬ")
	require.Contains(t, sent.texts[0], "/export")
	require.Contains(t, sent.markups[0], "d:exp")
	require.Contains(t, sent.markups[0], "Сначала прислать все документы")
}

func TestDisconnect_clearsKeyboardWithEmptyArray(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), fakeDrive{})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.True(t, has(sent.edits, "Google Диск отключён"), "edits %q", sent.edits)
	markups := sent.snapshot(&sent.editMarkups)
	require.NotEmpty(t, markups)
	last := markups[len(markups)-1]
	require.Contains(t, last, `"inline_keyboard":[]`, "an empty array clears the keyboard")
	require.NotContains(t, last, `"inline_keyboard":null`, "Telegram rejects a null inline_keyboard")
}
