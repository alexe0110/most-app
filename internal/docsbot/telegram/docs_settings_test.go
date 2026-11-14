package telegram_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestDeleteData_revokeFailureStillConfirms(t *testing.T) {
	b, sent, repo := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
	require.Empty(t, repo.Docs)
}

func TestDisconnect_driveFailureIsShown(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.False(t, has(sent.edits, "Google Диск отключён"), "the integration is still active: %q", sent.edits)
	require.True(t, has(sent.texts, "Что-то пошло не так"), "texts %q", sent.texts)
}

func TestDeleteData_revokeFailureIsWarnedAndConfirmed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	b, sent, _ := newBotWith(t, log, docsbottest.FakeDrive{RevokeErr: errors.New("google down"), DisconnectErr: errors.New("google down")})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Contains(t, buf.String(), `"level":"WARN"`)
	require.Contains(t, buf.String(), "drive revoke incomplete")
	require.NotContains(t, buf.String(), "delete all data", "not treated as a failure")
}

func TestStart_driveFolder(t *testing.T) {
	b, sent, _ := newBotOpts(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{}, telegram.WithDriveFolder("Most Docs Test"))

	b.ProcessUpdate(t.Context(), commandUpdate("start"))

	require.True(t, has(sent.texts, "«Most Docs Test»"), "sent %q", sent.texts)
}

func TestSettings_showsCountsAndControls(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatPassport, Title: "p", TgFileUniqueID: "u1"})
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Category: docsbot.CatOther, Title: "o", TgFileUniqueID: "u2"})

	b.ProcessUpdate(ctx, commandUpdate("settings"))

	require.True(t, has(sent.texts, "Google Диск"), "sent %q", sent.texts)
	require.True(t, has(sent.texts, "Документов: 2"), "sent %q", sent.texts)
}

func TestDeleteData_confirmation(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "p", TgFileUniqueID: "u1"})

	// Tapping "Удалить мои данные" asks for the exact word.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	require.True(t, has(sent.texts, "УДАЛИТЬ"), "prompt asks for the word: %q", sent.texts)

	// A wrong word cancels and leaves the data in place.
	b.ProcessUpdate(ctx, textUpdate("удалить"))
	require.True(t, has(sent.texts, "отменено"), "sent %q", sent.texts)
	require.Len(t, repo.Docs, 1, "data intact after a wrong word")

	// Re-arm, then the exact word deletes everything.
	b.ProcessUpdate(ctx, dataCallback("d:dd"))
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.True(t, has(sent.texts, "Данные удалены"), "sent %q", sent.texts)
	require.Empty(t, repo.Docs, "data deleted after the exact word")
}

func TestDeleteData_promptOffersExportFirst(t *testing.T) {
	b, sent, _ := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dd"))

	require.Len(t, sent.texts, 1)
	require.Contains(t, sent.texts[0], "УДАЛИТЬ")
	require.Contains(t, sent.texts[0], "/export")
	require.Contains(t, sent.markups[0], "d:exp")
	require.Contains(t, sent.markups[0], "Прислать все документы")
}

func TestDisconnect_clearsKeyboardWithEmptyArray(t *testing.T) {
	b, sent, _ := newBotWith(t, slog.New(slog.DiscardHandler), docsbottest.FakeDrive{})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	b.ProcessUpdate(ctx, dataCallback("d:dc"))

	require.True(t, has(sent.edits, "Google Диск отключён"), "edits %q", sent.edits)
	markups := sent.snapshot(&sent.editMarkups)
	require.NotEmpty(t, markups)
	last := markups[len(markups)-1]
	require.Contains(t, last, `"inline_keyboard":[]`, "an empty array clears the keyboard")
	require.NotContains(t, last, `"inline_keyboard":null`, "Telegram rejects a null inline_keyboard")
}

func startUpdate(payload string) *models.Update {
	u := textUpdate("/start " + payload)
	u.Message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len("/start")}}
	return u
}

func TestStart_recordsSource(t *testing.T) {
	yes := true
	tests := []struct {
		name    string
		payload string
		want    docsbot.EventProps
	}{
		{"campaign", "ad_1", docsbot.EventProps{Source: "ad_1", New: &yes}},
		{"no payload", "", docsbot.EventProps{New: &yes}},
		{"malformed payload", "Ad-1", docsbot.EventProps{New: &yes}},
		{"connected redirect is navigation", "connected", docsbot.EventProps{New: &yes}},
		{"inline button is navigation", "inline", docsbot.EventProps{New: &yes}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), startUpdate(tt.payload))

			require.Equal(t, []docsbot.Event{{User: docsbottest.TestUserID, Name: docsbot.EventStart, Props: tt.want}}, repo.Events)
			require.NotEmpty(t, sent.texts, "the greeting is sent as before")
		})
	}
}

func TestStart_eventFailureStillGreets(t *testing.T) {
	b, sent, repo := newBot(t)
	repo.EventsErr = errors.New("db down")

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), startUpdate("ad_1"))

	require.True(t, has(sent.texts, "Most Docs"), "sent %q", sent.texts)
}
