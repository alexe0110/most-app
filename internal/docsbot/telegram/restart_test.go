package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// The in-memory stores (renames, searches, origins, sessions) are empty after
// a restart. These tests press old buttons against empty stores.

func replyToRenamePrompt(chatID int64, text string) *models.Update {
	u := textUpdate(chatID, text)
	u.Message.ReplyToMessage = &models.Message{Text: "Пришли новое название документа."}
	return u
}

func TestRestart_renameReplyWithoutArmedRenameIsStale(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")

	b.ProcessUpdate(ctx, replyToRenamePrompt(100, "Новое имя"))

	require.Equal(t, []string{"Кнопка устарела, открой заново."}, sent.texts)
	require.Equal(t, "Старое", repo.docs[doc.ID].Title, "title untouched")
}

func TestRestart_plainTextStillSearches(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	seedPassport(repo, "Паспорт", "u1")

	b.ProcessUpdate(ctx, textUpdate(100, "паспорт"))

	require.True(t, has(sent.texts, "Нашёл"), "texts %q", sent.texts)
}

func TestRestart_callbacksWithoutStoreEntries(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт", "u1")
	id := string(doc.ID)

	// origins are lost: the card opens and goes back to the catalog.
	b.ProcessUpdate(ctx, dataCallback(100, "d:dn:"+id))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Категория:")

	// searches are lost: "back to search" falls back to the catalog.
	b.ProcessUpdate(ctx, dataCallback(100, "d:sb"))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Твои документы")

	// an album picker pressed after restart for an album that no longer exists.
	b.ProcessUpdate(ctx, dataCallback(100, "d:ga:missing:"+string(docsbot.CatPassport)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Этого документа уже нет")

	// the delete confirmation was never armed: "yes" on a missing document.
	b.ProcessUpdate(ctx, dataCallback(100, "d:dy:missing"))
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
