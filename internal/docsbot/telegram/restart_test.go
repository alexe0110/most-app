package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// The in-memory stores (renames, searches, origins, sessions) are empty after
// a restart. These tests press old buttons against empty stores.

func TestRestart_renameTextWithoutArmedRenameIsASearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")

	b.ProcessUpdate(ctx, textUpdateID(9, "Новое имя"))

	require.Equal(t, "Старое", repo.docs[doc.ID].Title, "title untouched")
	require.Zero(t, sent.deletes, "the message is not deleted")
	require.Len(t, sent.texts, 1, "answered as a search")
}

func TestRestart_plainTextStillSearches(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, textUpdate("паспорт"))

	require.True(t, has(sent.texts, "Нашёл"), "texts %q", sent.texts)
}

func TestRestart_callbacksWithoutStoreEntries(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт", "u1")
	id := string(doc.ID)

	// origins are lost: the card opens and goes back to the catalog.
	b.ProcessUpdate(ctx, dataCallback("d:dn:"+id))
	require.Contains(t, sent.edits[len(sent.edits)-1], "🗂 ")

	// searches are lost: "back to search" falls back to the catalog.
	b.ProcessUpdate(ctx, dataCallback("d:sb"))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Твои документы")

	// an album picker pressed after restart for an album that no longer exists.
	b.ProcessUpdate(ctx, dataCallback("d:ga:missing:"+string(docsbot.CatPassport)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Этого документа уже нет")

	// the delete confirmation was never armed: "yes" on a missing document.
	b.ProcessUpdate(ctx, dataCallback("d:dy:missing"))
	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
}

func TestRestart_convertCallbackWithoutSession(t *testing.T) {
	for _, data := range []string{"conv:pdf", "conv:compress", "conv:done", "conv:cancel"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)

			h.bot.ProcessUpdate(t.Context(), callbackUpdate(data))

			require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
		})
	}
}

func TestRestart_deleteWordWithoutArmedConfirmationIsASearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))

	require.Contains(t, repo.docs, doc.ID, "nothing is deleted on a lost confirmation")
	require.False(t, has(sent.texts, "Данные удалены"), "texts %q", sent.texts)
}
