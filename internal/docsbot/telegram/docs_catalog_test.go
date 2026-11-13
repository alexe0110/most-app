package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestSearch_findsSavedDocument(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	update := &models.Update{Message: &models.Message{
		From: &models.User{ID: 100},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Text: "паспорт",
	}}
	b.ProcessUpdate(ctx, update)

	require.True(t, has(sent.texts, "Нашёл"), "sent %q", sent.texts)
}

func TestSearchBack_rerunsLastSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	b.ProcessUpdate(ctx, textUpdate("паспорт"))

	for range 2 { // the query survives repeated "Назад"
		b.ProcessUpdate(ctx, dataCallback("d:sb"))
	}

	require.Len(t, sent.edits, 2)
	require.True(t, has(sent.edits, "Нашёл (1):"), "edits %q", sent.edits)
}

func TestSearchBack_withoutQueryFallsBackToCatalog(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:sb"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Твои документы")
	require.Contains(t, sent.editMarkups[0], "d:lc:"+string(docsbot.CatPassport))
	require.Empty(t, sent.texts)
}
