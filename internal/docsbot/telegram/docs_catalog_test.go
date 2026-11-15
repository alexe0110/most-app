package telegram_test

import (
	"errors"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestSearch_blankQueryShowsHint(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Title: "Паспорт", Category: docsbot.CatPassport})

	b.ProcessUpdate(ctx, textUpdate("   "))

	require.True(t, has(sent.texts, "Напиши, что искать"), "sent %q", sent.texts)
	require.False(t, has(sent.texts, "ничего нет"))
}

func TestSearch_findsSavedDocument(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	update := &models.Update{Message: &models.Message{
		From: &models.User{ID: 100},
		Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate},
		Text: "паспорт",
	}}
	b.ProcessUpdate(ctx, update)

	require.True(t, has(sent.texts, "Нашёл"), "sent %q", sent.texts)
	require.Equal(t, []docsbot.Event{{User: docsbottest.TestUserID, Name: docsbot.EventSearch}}, repo.Events,
		"the event carries no query text")
}

func TestSearch_eventFailureStillAnswers(t *testing.T) {
	b, sent, repo := newBot(t)
	repo.EventsErr = errors.New("db down")
	seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), textUpdate("паспорт"))

	require.True(t, has(sent.texts, "Нашёл"), "sent %q", sent.texts)
	require.Empty(t, repo.Events)
}

func TestSearch_blankQueryIsNotRecorded(t *testing.T) {
	b, _, repo := newBot(t)

	b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), textUpdate("   "))

	require.Empty(t, repo.Events)
}

func TestSearchBack_rerunsLastSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})
	b.ProcessUpdate(ctx, textUpdate("паспорт"))

	for range 2 { // the query survives repeated "Назад"
		b.ProcessUpdate(ctx, dataCallback("d:sb"))
	}

	require.Len(t, sent.edits, 2)
	require.True(t, has(sent.edits, "Нашёл 1 документ:"), "edits %q", sent.edits)
	require.Equal(t, []docsbot.EventName{docsbot.EventSearch}, repo.EventNames(), "redrawing the results is not a new search")
}

func TestSearchBack_withoutQueryFallsBackToCatalog(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:sb"))

	require.Len(t, sent.edits, 1)
	require.Contains(t, sent.edits[0], "Твои документы")
	require.Contains(t, sent.editMarkups[0], "d:lc:"+string(docsbot.CatPassport))
	require.Empty(t, sent.texts)
}
