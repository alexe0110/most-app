package telegram_test

import (
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestRegister_routing(t *testing.T) {
	tests := []struct {
		name     string
		update   *models.Update
		wantText string
	}{
		{"start", commandUpdate(100, "start"), "«Most Docs»"},
		{"help", commandUpdate(100, "help"), "умею"},
		{"unknown command", commandUpdate(100, "foo"), "Не понимаю"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, _ := newBot(t)
			b.ProcessUpdate(t.Context(), tt.update)
			require.True(t, has(sent.texts, tt.wantText), "sent %q", sent.texts)
		})
	}
}

func TestCallback_inaccessibleMessageChangesNothing(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Паспорт", "u1")
	id, cat := string(doc.ID), string(docsbot.CatPassport)
	before := repo.docs[doc.ID]

	// Telegram stops giving old messages: a press has no message to edit.
	for _, data := range []string{
		"d:cat:" + id + ":" + cat, "d:ga:" + id + ":" + cat, "d:gpk:" + id, "d:sc:" + id + ":" + cat,
		"d:chc:" + id, "d:snd:" + id, "d:rn:" + id, "d:rnc:" + id, "d:del:" + id, "d:dy:" + id, "d:dn:" + id,
		"d:lc:" + cat + ":1", "d:lb", "d:tr", "d:try", "d:trn", "d:rt:" + id, "d:rs:" + id, "d:dc", "d:rta",
		"d:exp", "d:dd", "d:rsm:sm", "d:oc:" + id + ":s", "d:sb", "d:inl:0", "d:zzz", "d:",
	} {
		t.Run(data, func(t *testing.T) {
			answers := len(sent.answers)

			b.ProcessUpdate(ctx, inaccessible(dataCallback(data)))

			require.Empty(t, sent.edits)
			require.Empty(t, sent.texts)
			require.Empty(t, sent.files)
			require.Empty(t, sent.markupEdits)
			require.Zero(t, sent.deletes)
			require.Len(t, sent.answers, answers+1, "the button stops spinning, once")
			require.Equal(t, before, repo.docs[doc.ID], "the document is untouched")
		})
	}

	// "d:dd" must not have armed the deletion: the confirmation word is a search.
	b.ProcessUpdate(ctx, textUpdate("УДАЛИТЬ"))
	require.Contains(t, repo.docs, doc.ID)
	require.False(t, has(sent.texts, "удалены"), "texts %q", sent.texts)
}
