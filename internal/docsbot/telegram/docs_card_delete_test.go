package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestDeleteYes_returnsToOrigin(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	seed := func(title, uid string) docsbot.Document {
		return repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
			Category: docsbot.CatPassport, Title: title, TgFileID: uid, TgFileUniqueID: uid,
		})
	}
	first, second := seed("Первый", "u1"), seed("Второй", "u2")
	open := func(id docsbot.DocumentID) {
		b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(id)+":l:"+string(docsbot.CatPassport)+":1"))
	}

	open(first.ID)
	b.ProcessUpdate(ctx, dataCallback("d:del:"+string(first.ID)))
	require.True(t, has(sent.edits, "Удалить «Первый»?"), "edits %q", sent.edits)
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(first.ID)))
	require.True(t, has(sent.edits, docsbot.CatPassport.Label()+" · 1 документ"), "category page: %q", sent.edits)

	open(second.ID)
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(second.ID)))
	require.True(t, has(sent.edits, "Пока пусто"), "catalog when empty: %q", sent.edits)
	require.Empty(t, sent.texts, "everything is edited in place")
}

func TestDeleteYes_withoutOriginClearsKeyboard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument,
		Category: docsbot.CatPassport, Title: "Паспорт РК", TgFileID: "f", TgFileUniqueID: "u",
	})

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(doc.ID)))

	require.True(t, has(sent.edits, "Документ удалён"), "edits %q", sent.edits)
	last := sent.editMarkups[len(sent.editMarkups)-1]
	require.NotEmpty(t, last, "reply_markup must be sent to clear the old keyboard")
	require.Contains(t, last, "Мои документы")
	require.Contains(t, last, `"d:lb"`)
	for _, cbData := range []string{"d:snd", "d:rn", "d:sc", "d:del", "d:oc"} {
		require.NotContains(t, last, cbData, "no card buttons remain")
	}
}

func TestDeleteYes_fromSearchReturnsToSearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	keep := seedPassport(repo, "Паспорт один", "u1")
	gone := seedPassport(repo, "Паспорт два", "u2")
	b.ProcessUpdate(ctx, textUpdate("паспорт"))

	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(gone.ID)+":s"))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(gone.ID)))

	require.Equal(t, "Нашёл 1 документ:", sent.edits[len(sent.edits)-1])
	require.NotContains(t, repo.Docs, gone.ID)
	require.Contains(t, repo.Docs, keep.ID)
}

func TestDeleteNo_restoresCardAndStaleIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	doc := seedPassport(repo, "Паспорт РК", "u1")

	b.ProcessUpdate(ctx, dataCallback("d:dn:"+string(doc.ID)))
	require.Contains(t, sent.edits[0], "🗂 ")

	b.ProcessUpdate(ctx, dataCallback("d:dn:missing"))
	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
}

func TestDeleteYes_forgetsTheOriginOfTheMessage(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	gone := seedPassport(repo, "Первый", "u1")
	kept := seedPassport(repo, "Второй", "u2")
	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(gone.ID)+":l:"+string(docsbot.CatPassport)+":1"))
	b.ProcessUpdate(ctx, dataCallback("d:dy:"+string(gone.ID)))

	// The same message shows another card later (a stale card button pressed on
	// it): it must not inherit the way back of the card that was deleted.
	b.ProcessUpdate(ctx, dataCallback("d:dn:"+string(kept.ID)))

	last := sent.editMarkups[len(sent.editMarkups)-1]
	require.Contains(t, last, "d:snd:"+string(kept.ID), "the card is drawn")
	require.NotContains(t, last, "⬅️ Назад", "no way back to a list the deleted card came from")
}
