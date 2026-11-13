package telegram_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func inlineUpdate(query string) *models.Update {
	return &models.Update{InlineQuery: &models.InlineQuery{
		ID: "iq1", From: &models.User{ID: 100}, Query: query,
	}}
}

type inlineResultJSON struct {
	Type           string `json:"type"`
	ID             string `json:"id"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Caption        string `json:"caption"`
	PhotoFileID    string `json:"photo_file_id"`
	DocumentFileID string `json:"document_file_id"`
}

func decodeResults(t *testing.T, raw string) []inlineResultJSON {
	t.Helper()
	var out []inlineResultJSON
	require.NoError(t, json.Unmarshal([]byte(raw), &out))
	return out
}

func TestInline_ownDocuments(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	photo := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto, Category: docsbot.CatPassport,
		Title: "Паспорт", TgFileID: "photo-file", FileSize: 2048,
	})
	pdf := repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatCertificates,
		Title: "Справка", TgFileID: "doc-file", MIME: "application/pdf",
	})
	repo.seed(docsbot.Document{UserID: "other", Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Title: "Чужой", TgFileID: "foreign"})

	b.ProcessUpdate(ctx, inlineUpdate(""))

	require.Len(t, sent.inlines, 1)
	form := sent.inlines[0]
	require.Equal(t, "iq1", form.Get("inline_query_id"))
	require.Equal(t, "true", form.Get("is_personal"))
	require.Equal(t, "5", form.Get("cache_time"))
	require.Empty(t, form.Get("next_offset"))
	require.Empty(t, form.Get("button"))

	res := decodeResults(t, form.Get("results"))
	require.Len(t, res, 2)
	byID := map[string]inlineResultJSON{}
	for _, r := range res {
		byID[r.ID] = r
	}
	require.Equal(t, "photo", byID[string(photo.ID)].Type)
	require.Equal(t, "photo-file", byID[string(photo.ID)].PhotoFileID)
	require.Equal(t, "Паспорт", byID[string(photo.ID)].Title)
	require.Equal(t, "Паспорт и ID · Фото · 2 КБ", byID[string(photo.ID)].Description)
	require.Equal(t, "document", byID[string(pdf.ID)].Type)
	require.Equal(t, "doc-file", byID[string(pdf.ID)].DocumentFileID)
	require.Equal(t, "Справки · PDF", byID[string(pdf.ID)].Description)
	for _, r := range res {
		require.Empty(t, r.Caption)
	}
}

func TestInline_nextOffset(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	for range 21 {
		repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Title: "d", TgFileID: "f"})
	}

	b.ProcessUpdate(ctx, inlineUpdate("d"))

	require.Len(t, decodeResults(t, sent.inlines[0].Get("results")), 20)
	require.Equal(t, "20", sent.inlines[0].Get("next_offset"))
}

func TestInline_buttons(t *testing.T) {
	tests := []struct {
		name       string
		ctxUser    bool
		seed       bool
		disabled   bool
		wantButton string
	}{
		{"unknown user", false, false, false, "Открыть Most Files"},
		{"no documents", true, false, false, "Добавить документ"},
		{"disabled", true, true, true, "Включить в настройках"},
		{"has documents", true, true, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := t.Context()
			if tt.ctxUser {
				ctx = tg.ContextWithUserID(ctx, testUserID)
			}
			if tt.seed {
				repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Title: "d", TgFileID: "f"})
			}
			if tt.disabled {
				repo.inlineOff = map[docsbot.UserID]bool{testUserID: true}
			}

			b.ProcessUpdate(ctx, inlineUpdate(""))

			require.Len(t, sent.inlines, 1)
			raw := sent.inlines[0].Get("button")
			if tt.wantButton == "" {
				require.Empty(t, raw)
				return
			}
			var btn struct {
				Text           string `json:"text"`
				StartParameter string `json:"start_parameter"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &btn))
			require.Equal(t, tt.wantButton, btn.Text)
			require.Equal(t, "inline", btn.StartParameter)
			if !tt.seed || tt.disabled {
				require.JSONEq(t, "[]", sent.inlines[0].Get("results"))
			}
		})
	}
}

func TestInline_logsNoContent(t *testing.T) {
	var buf bytes.Buffer
	b, _, repo := newBotWithLog(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Title: "СекретныйТитул", TgFileID: "secret-file-id"})

	b.ProcessUpdate(ctx, inlineUpdate("СекретныйЗапрос"))

	require.Contains(t, buf.String(), "inline answered")
	require.NotContains(t, buf.String(), "СекретныйТитул")
	require.NotContains(t, buf.String(), "СекретныйЗапрос")
	require.NotContains(t, buf.String(), "secret-file-id")
}

func TestSettings_inlineToggle(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, commandUpdate(100, "settings"))
	require.True(t, has(sent.markups, "Отправка из любого чата: вкл"), "markups %q", sent.markups)
	require.True(t, has(sent.markups, "d:inl:0"), "markups %q", sent.markups)

	b.ProcessUpdate(ctx, dataCallback("d:inl:0"))
	require.True(t, repo.inlineOff[testUserID])
	require.True(t, has(sent.editMarkups, "Отправка из любого чата: выкл"), "edits %q", sent.editMarkups)
	require.True(t, has(sent.editMarkups, "d:inl:1"), "edits %q", sent.editMarkups)
	require.Equal(t, []int{7}, sent.editIDs, "settings message is edited in place")

	b.ProcessUpdate(ctx, dataCallback("d:inl:1"))
	require.False(t, repo.inlineOff[testUserID])
	require.True(t, has(sent.editMarkups, "Отправка из любого чата: вкл"))
}

func TestStart_inlinePayload(t *testing.T) {
	start := func(text string) *models.Update {
		u := textUpdate(text)
		u.Message.Entities = []models.MessageEntity{{Type: models.MessageEntityTypeBotCommand, Offset: 0, Length: len("/start")}}
		return u
	}

	t.Run("enabled: normal start", func(t *testing.T) {
		b, sent, _ := newBot(t)
		b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), start("/start inline"))
		require.True(t, has(sent.texts, "Most Docs"), "sent %q", sent.texts)
		require.False(t, has(sent.texts, "Google Диск:"), "sent %q", sent.texts)
	})

	t.Run("disabled: settings screen", func(t *testing.T) {
		b, sent, repo := newBot(t)
		repo.inlineOff = map[docsbot.UserID]bool{testUserID: true}
		b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), start("/start inline"))
		require.True(t, has(sent.texts, "Google Диск:"), "sent %q", sent.texts)
		require.True(t, has(sent.markups, "Отправка из любого чата: выкл"), "markups %q", sent.markups)
	})

	t.Run("plain start unaffected when disabled", func(t *testing.T) {
		b, sent, repo := newBot(t)
		repo.inlineOff = map[docsbot.UserID]bool{testUserID: true}
		b.ProcessUpdate(tg.ContextWithUserID(t.Context(), testUserID), start("/start"))
		require.True(t, has(sent.texts, "Most Docs"), "sent %q", sent.texts)
	})
}

func TestInline_disabledUserWithDocumentsGetsNothing(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Title: "d", TgFileID: "f"})
	repo.inlineOff = map[docsbot.UserID]bool{testUserID: true}

	b.ProcessUpdate(ctx, inlineUpdate("d"))

	require.Len(t, sent.inlines, 1)
	require.JSONEq(t, "[]", sent.inlines[0].Get("results"))
	require.Empty(t, sent.inlines[0].Get("next_offset"))
}

func TestSettings_inlineToggleStale(t *testing.T) {
	t.Run("repeated tap is idempotent", func(t *testing.T) {
		b, _, repo := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		b.ProcessUpdate(ctx, dataCallback("d:inl:0"))
		b.ProcessUpdate(ctx, dataCallback("d:inl:0"))
		require.True(t, repo.inlineOff[testUserID])
	})

	t.Run("inaccessible message changes nothing", func(t *testing.T) {
		b, _, repo := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		u := dataCallback("d:inl:0")
		u.CallbackQuery.Message = models.MaybeInaccessibleMessage{
			Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
			InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		}
		b.ProcessUpdate(ctx, u)
		require.False(t, repo.inlineOff[testUserID])
	})

	t.Run("malformed payload does not panic", func(t *testing.T) {
		b, sent, repo := newBot(t)
		ctx := tg.ContextWithUserID(t.Context(), testUserID)
		for _, data := range []string{"d:inl", "d:inl:x", "d:inl:"} {
			require.NotPanics(t, func() { b.ProcessUpdate(ctx, dataCallback(data)) })
		}
		require.False(t, repo.inlineOff[testUserID], "state unchanged")
		require.Empty(t, sent.edits, "nothing redrawn")
		require.Len(t, sent.answers, 3, "every tap is answered")
	})
}

func TestInline_serviceErrorStillAnswers(t *testing.T) {
	b, sent, repo := newBot(t)
	repo.inlineErr = errors.New("db down")
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, inlineUpdate("q"))

	require.Len(t, sent.inlines, 1)
	require.JSONEq(t, "[]", sent.inlines[0].Get("results"))
	require.Empty(t, sent.inlines[0].Get("button"))
}
