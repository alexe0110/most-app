package telegram_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
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
	require.True(t, has(sent.edits, fmt.Sprintf(telegram.TextFoundTitle, 1, telegram.PluralDocumentOne)), "edits %q", sent.edits)
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

func TestList_showsCatalogOrHintWhenEmpty(t *testing.T) {
	tests := []struct {
		name string
		seed bool
		want string
	}{
		{name: "empty library", want: telegram.TextCatalogEmpty},
		{name: "saved documents", seed: true, want: telegram.TextCatalogTitle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			if tt.seed {
				seedPassport(repo, "Паспорт", "u1")
			}

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), commandUpdate("list"))

			require.Len(t, sent.texts, 1)
			require.Contains(t, sent.texts[0], tt.want)
		})
	}
}

func TestListCat(t *testing.T) {
	const total = 11 // one more than a page
	title := func(n int, plural string) string {
		return fmt.Sprintf(telegram.TextCategoryTitle, docsbot.CatPassport.Label(), n, plural)
	}
	tests := []struct {
		name       string
		docs       int
		data       string
		press      func(*models.Update) *models.Update
		wantEdit   string // empty: nothing is edited
		wantMarkup []string
		noMarkup   []string
	}{
		{name: "first page links to the next", docs: total, data: "d:lc:passport:1",
			wantEdit: title(total, telegram.PluralDocumentMany), wantMarkup: []string{"d:lc:passport:2"}, noMarkup: []string{"d:lc:passport:0"}},
		{name: "last page links back", docs: total, data: "d:lc:passport:2",
			wantEdit: title(total, telegram.PluralDocumentMany), wantMarkup: []string{"d:lc:passport:1"}, noMarkup: []string{"d:lc:passport:3"}},
		{name: "page past the end shows the last page", docs: total, data: "d:lc:passport:99",
			wantEdit: title(total, telegram.PluralDocumentMany), wantMarkup: []string{"d:lc:passport:1"}},
		{name: "page below one shows the first page", docs: total, data: "d:lc:passport:-5",
			wantEdit: title(total, telegram.PluralDocumentMany), wantMarkup: []string{"d:lc:passport:2"}},
		{name: "unreadable page shows the first page", docs: total, data: "d:lc:passport:x",
			wantEdit: title(total, telegram.PluralDocumentMany), wantMarkup: []string{"d:lc:passport:2"}},
		{name: "category without documents", data: "d:lc:passport:1",
			wantEdit: title(0, telegram.PluralDocumentMany), wantMarkup: []string{"d:lb"}},
		{name: "single document", docs: 1, data: "d:lc:passport:1",
			wantEdit: title(1, telegram.PluralDocumentOne), noMarkup: []string{"d:lc:passport:2"}},
		{name: "unknown category is ignored", docs: 1, data: "d:lc:nonsense:1"},
		{name: "missing page is ignored", docs: 1, data: "d:lc:passport"},
		{name: "message too old to edit", docs: 1, data: "d:lc:passport:1", press: inaccessible},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			for i := range tt.docs {
				seedPassport(repo, fmt.Sprintf("Паспорт %d", i), fmt.Sprintf("u%d", i))
			}
			update := dataCallback(tt.data)
			if tt.press != nil {
				update = tt.press(update)
			}

			b.ProcessUpdate(tg.ContextWithUserID(t.Context(), docsbottest.TestUserID), update)

			if tt.wantEdit == "" {
				require.Empty(t, sent.edits)
				return
			}
			require.Len(t, sent.edits, 1)
			require.Contains(t, sent.edits[0], tt.wantEdit)
			for _, want := range tt.wantMarkup {
				require.Contains(t, sent.editMarkups[0], want)
			}
			for _, not := range tt.noMarkup {
				require.NotContains(t, sent.editMarkups[0], not)
			}
		})
	}
}
