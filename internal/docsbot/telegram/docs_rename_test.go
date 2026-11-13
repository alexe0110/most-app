package telegram_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestRename_editsCardInPlace(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":l:"+string(docsbot.CatPassport)+":1"))

	b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))

	prompt := sent.edits[len(sent.edits)-1]
	require.Equal(t, "Пришли новое название", prompt, "the card message turns into the prompt")
	require.Equal(t, "d:rnc:"+string(doc.ID), cancelData(t, sent.editMarkups[len(sent.editMarkups)-1]))
	require.Empty(t, sent.texts, "no separate prompt message, no force reply")

	b.ProcessUpdate(ctx, textUpdateID(55, "Новое имя"))

	require.Empty(t, sent.texts, "no new message at all")
	require.Equal(t, "Новое имя", repo.docs[doc.ID].Title)
	require.Equal(t, []int{55}, sent.deleteIDs, "the user's message is removed")
	require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1], "the same message is edited")
	require.Contains(t, sent.edits[len(sent.edits)-1], "Новое имя")
	require.True(t, has(sent.editMarkups, "d:lc:"+string(docsbot.CatPassport)+":1"), "origin kept: %q", sent.editMarkups)
}

// cancelData extracts the callback data of the only button of a prompt keyboard.
func cancelData(t *testing.T, markup string) string {
	t.Helper()
	var kb models.InlineKeyboardMarkup
	require.NoError(t, json.Unmarshal([]byte(markup), &kb))
	require.Len(t, kb.InlineKeyboard, 1)
	require.Len(t, kb.InlineKeyboard[0], 1)
	require.Equal(t, "Отмена", kb.InlineKeyboard[0][0].Text)
	return kb.InlineKeyboard[0][0].CallbackData
}

func TestRename_invalidTitleKeepsWaiting(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantHint string
	}{
		{"blank", " \n\u200b ", "Название пустое"},
		{"too long", strings.Repeat("я", docsbot.MaxTitleRunes+1), "слишком длинное"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := seedPassport(repo, "Старое", "u1")
			b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))

			b.ProcessUpdate(ctx, textUpdateID(55, tt.text))

			require.Contains(t, sent.edits[len(sent.edits)-1], tt.wantHint, "hint in the same message")
			require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1])
			require.Equal(t, "d:rnc:"+string(doc.ID), cancelData(t, sent.editMarkups[len(sent.editMarkups)-1]))
			require.Empty(t, sent.texts)
			require.Equal(t, "Старое", repo.docs[doc.ID].Title)

			// The state is kept: the next valid name goes through.
			b.ProcessUpdate(ctx, textUpdateID(56, "Хорошее имя"))
			require.Equal(t, "Хорошее имя", repo.docs[doc.ID].Title)
			require.Equal(t, []int{55, 56}, sent.deleteIDs)
		})
	}
}

func TestRename_cancelRestoresCardAndIsRepeatable(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback("d:oc:"+string(doc.ID)+":s"))
	b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))

	b.ProcessUpdate(ctx, dataCallback("d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое", "card is back")
	require.True(t, has(sent.editMarkups[len(sent.editMarkups)-1:], "d:sb"), "with its Назад")

	// A repeated tap is harmless: it just redraws the card.
	b.ProcessUpdate(ctx, dataCallback("d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")

	// The state is gone: a text is a search, not a rename.
	b.ProcessUpdate(ctx, textUpdateID(60, "Новое"))
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
	require.Empty(t, sent.deleteIDs)
	require.True(t, has(sent.texts, "Ничего не нашёл") || has(sent.texts, "Нашёл"), "texts %q", sent.texts)
}

func TestRename_staleCancelHasNoSideEffects(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	other := seedPassport(repo, "Другое", "u2")
	// B is armed on message 8; a stale cancel of message 7 must not drop it.
	b.ProcessUpdate(ctx, dataCallbackMsg(8, "d:rn:"+string(other.ID)))

	b.ProcessUpdate(ctx, dataCallbackMsg(7, "d:rnc:"+string(doc.ID)))

	require.Equal(t, 7, sent.editIDs[len(sent.editIDs)-1])
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")
	b.ProcessUpdate(ctx, textUpdateID(61, "Имя для второго"))
	require.Equal(t, "Имя для второго", repo.docs[other.ID].Title, "the newer prompt is still armed")
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
}

func TestRename_otherButtonRestoresPromptCard(t *testing.T) {
	tests := []struct {
		name     string
		callback func(doc docsbot.Document) string
	}{
		{"open another card", func(docsbot.Document) string { return "d:lb" }},
		{"delete ask", func(d docsbot.Document) string { return "d:del:" + string(d.ID) }},
		{"category", func(d docsbot.Document) string { return "d:chc:" + string(d.ID) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			doc := seedPassport(repo, "Старое", "u1")
			other := seedPassport(repo, "Другое", "u2")
			b.ProcessUpdate(ctx, dataCallbackMsg(7, "d:rn:"+string(doc.ID)))
			sent.edits, sent.editIDs = nil, nil

			b.ProcessUpdate(ctx, dataCallbackMsg(9, tt.callback(other)))

			require.Equal(t, 7, sent.editIDs[0], "the prompt message is returned to its card first")
			require.Contains(t, sent.edits[0], "Старое")
			// And the text is no longer a rename.
			b.ProcessUpdate(ctx, textUpdateID(62, "Не имя"))
			require.Equal(t, "Старое", repo.docs[doc.ID].Title)
			require.Empty(t, sent.deleteIDs)
		})
	}
}

func TestRename_newPromptRestoresPreviousCard(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	a := seedPassport(repo, "Первое", "u1")
	c := seedPassport(repo, "Второе", "u2")
	b.ProcessUpdate(ctx, dataCallbackMsg(7, "d:rn:"+string(a.ID)))
	sent.edits, sent.editIDs = nil, nil

	b.ProcessUpdate(ctx, dataCallbackMsg(9, "d:rn:"+string(c.ID)))

	require.Equal(t, []int{7, 9}, sent.editIDs)
	require.Contains(t, sent.edits[0], "Первое", "the first message is a card again")
	require.Equal(t, "Пришли новое название", sent.edits[1])
	b.ProcessUpdate(ctx, textUpdateID(63, "Новое для второго"))
	require.Equal(t, "Новое для второго", repo.docs[c.ID].Title)
	require.Equal(t, "Первое", repo.docs[a.ID].Title)
}

func TestRename_expiredStateIsASearch(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")

	// No armed rename (restart or TTL): the text goes to search, nothing is renamed or deleted.
	b.ProcessUpdate(ctx, textUpdateID(64, "Новое имя"))

	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
	require.Empty(t, sent.deleteIDs)
	require.Len(t, sent.texts, 1)

	// A cancel on the stale prompt only restores the card.
	b.ProcessUpdate(ctx, dataCallback("d:rnc:"+string(doc.ID)))
	require.Contains(t, sent.edits[len(sent.edits)-1], "Старое")
	require.Equal(t, "Старое", repo.docs[doc.ID].Title)
}

func TestRename_deletedDocumentIsGone(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	doc := seedPassport(repo, "Старое", "u1")
	b.ProcessUpdate(ctx, dataCallback("d:rn:"+string(doc.ID)))
	delete(repo.docs, doc.ID)

	b.ProcessUpdate(ctx, textUpdateID(65, "Новое"))

	require.True(t, has(sent.edits, "Этого документа уже нет"), "edits %q", sent.edits)
	require.False(t, has(sent.texts, "Что-то пошло не так"))
}
