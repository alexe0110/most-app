package converter_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

var replaceToken = regexp.MustCompile(`conv:rp:([0-9a-f]+)`)

// resultFromCard compresses the card's photo and returns the result's token.
func resultFromCard(t *testing.T, h *convHarness, id docsbot.DocumentID) string {
	t.Helper()
	tok := compressFromMenu(t, h, id)
	m := replaceToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m, "a result from a card offers to replace the original: %q", h.docMarkups[0])
	require.Equal(t, tok, m[1])
	return tok
}

func lastCaption(h *convHarness) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.editCaptions) == 0 {
		return ""
	}
	return h.editCaptions[len(h.editCaptions)-1]
}

func TestConvertReplace_commandResultKeyboard(t *testing.T) {
	h := newConvHarness(t)
	compressFromCommand(t, h)

	require.Contains(t, h.docMarkups[0], "Сохранить сжатый")
	require.Contains(t, h.docMarkups[0], "Ничего не делать")
	require.Contains(t, h.docMarkups[0], "conv:sv:")
	require.NotContains(t, h.docMarkups[0], "conv:rp:")
	require.NotContains(t, h.docMarkups[0], "Заменить")
}

func TestConvertReplace_keyboard(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	resultFromCard(t, h, src.ID)

	require.Contains(t, h.docMarkups[0], "Заменить сжатым")
	require.Contains(t, h.docMarkups[0], "Ничего не делать")
	require.Contains(t, h.docMarkups[0], "conv:skip:")
	require.NotContains(t, h.docMarkups[0], "Сохранить")
	require.NotContains(t, h.docMarkups[0], "conv:sv:")
	require.NotContains(t, h.docMarkups[0], "Сохранить как новый")
}

func TestConvertReplace_askThenConfirm(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok))

	require.Contains(t, lastCaption(h), "Заменить оригинал этим файлом?")
	require.Contains(t, lastCaption(h), "conv:rpy:"+tok)
	require.Contains(t, lastCaption(h), "conv:rpn:"+tok)
	require.Equal(t, src, h.repo.Docs[src.ID], "asking changes nothing")

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok)) // double tap

	got := h.repo.Docs[src.ID]
	require.Len(t, h.repo.Docs, 1, "no new document")
	require.Equal(t, "res-0", got.TgFileID)
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, src.Title, got.Title)
	require.Equal(t, src.Category, got.Category)
	require.Equal(t, src.MetaVersion+1, got.MetaVersion, "replaced once, however many taps")
	require.Contains(t, h.lastText(), "Эта кнопка уже не работает", "the second tap is stale")
}

func TestConvertReplace_cancelRestoresButtons(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpn:"+tok))

	require.Contains(t, lastCaption(h), "Заменить сжатым")
	require.Contains(t, lastCaption(h), "conv:rp:"+tok)
	require.Contains(t, lastCaption(h), "conv:skip:"+tok)
	require.Equal(t, src, h.repo.Docs[src.ID])

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok)) // the choice is still alive
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	require.Equal(t, "res-uniq-0", h.repo.Docs[src.ID].TgFileUniqueID)
}

func TestConvertReplace_staleToken(t *testing.T) {
	for _, data := range []string{"conv:rp:", "conv:rpy:", "conv:rpn:"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedSource(h, docsbot.StatusSaved)
			resultFromCard(t, h, src.ID)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate(data+"deadbeef"))

			require.Contains(t, h.lastText(), "Эта кнопка уже не работает")
			require.Equal(t, src, h.repo.Docs[src.ID])
		})
	}
}

func TestConvertReplace_afterSkipIsStale(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Contains(t, h.lastText(), "Эта кнопка уже не работает")
	require.Equal(t, src, h.repo.Docs[src.ID])
}

func TestConvertReplace_refusals(t *testing.T) {
	tests := []struct {
		name string
		mod  func(d *docsbot.Document)
		want string
	}{
		{"uploading", func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupUploading }, "ещё сохраняется"},
		{"sync in flight", func(d *docsbot.Document) {
			d.BackupStatus, d.SyncStatus = docsbot.BackupBackedUp, docsbot.SyncPending
		}, "ещё сохраняется"},
		{"drive file missing", func(d *docsbot.Document) {
			d.BackupStatus, d.SyncStatus = docsbot.BackupBackedUp, docsbot.SyncMissing
		}, "Загрузить заново"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedSource(h, docsbot.StatusSaved)
			tok := resultFromCard(t, h, src.ID)
			d := h.repo.Docs[src.ID]
			tt.mod(&d)
			h.repo.Docs[src.ID] = d
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

			require.Contains(t, h.lastText(), tt.want)
			require.Equal(t, d, h.repo.Docs[src.ID], "nothing changed")
			require.Contains(t, lastCaption(h), "conv:rp:"+tok, "buttons are back")

			// The user can retry once the document is free again.
			d.BackupStatus, d.SyncStatus = docsbot.BackupBackedUp, docsbot.SyncOK
			h.repo.Docs[src.ID] = d
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
			require.Equal(t, "res-uniq-0", h.repo.Docs[src.ID].TgFileUniqueID)
		})
	}
}

func TestConvertReplace_duplicateFileShowsExistingCard(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Уже есть", TgFileID: "x", TgFileUniqueID: "res-uniq-0",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Equal(t, src, h.repo.Docs[src.ID])
	require.Contains(t, h.allTexts(), "Уже есть")
}

func TestConvertReplace_goneDocument(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	delete(h.repo.Docs, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
}

// resultButtons reads the keyboard of the first result the bot sent.
func resultButtons(t *testing.T, h *convHarness) [][]models.InlineKeyboardButton {
	t.Helper()
	var kb models.InlineKeyboardMarkup
	require.NoError(t, json.Unmarshal([]byte(h.docMarkups[0]), &kb))
	return kb.InlineKeyboard
}

func TestConvertResult_buttonsFollowSourceAndKind(t *testing.T) {
	compress := func(t *testing.T, h *convHarness, status docsbot.Status) {
		src := seedSource(h, status)
		ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
		h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":c1"))
	}
	rotate := func(t *testing.T, h *convHarness, status docsbot.Status) {
		src := seedPDF(t, h)
		src.Status = status
		h.repo.Docs[src.ID] = src
		ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
		h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":rot"))
	}
	tests := []struct {
		name   string
		run    func(t *testing.T, h *convHarness, status docsbot.Status)
		status docsbot.Status
		save   string // label of the first button
		action string // its callback action
	}{
		{"compress, saved source", compress, docsbot.StatusSaved, "♻️ Заменить сжатым", "conv:rp:"},
		{"compress, pending source", compress, docsbot.StatusPending, "💾 Сохранить сжатый", "conv:sv:"},
		{"pdf, saved source", rotate, docsbot.StatusSaved, "♻️ Заменить оригинал", "conv:rp:"},
		{"pdf, pending source", rotate, docsbot.StatusPending, "💾 Сохранить PDF", "conv:sv:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			tt.run(t, h, tt.status)

			require.Len(t, h.docs, 1)
			rows := resultButtons(t, h)
			require.Len(t, rows, 2)
			require.Len(t, rows[0], 1)
			require.Equal(t, tt.save, rows[0][0].Text)
			require.True(t, strings.HasPrefix(rows[0][0].CallbackData, tt.action), rows[0][0].CallbackData)
			require.Len(t, rows[1], 1)
			require.Equal(t, "Ничего не делать", rows[1][0].Text)
			require.True(t, strings.HasPrefix(rows[1][0].CallbackData, "conv:skip:"), rows[1][0].CallbackData)
		})
	}
}

func TestConvertResult_sourceGoneBeforeTheResultOffersToSave(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))
	delete(h.repo.Docs, src.ID)

	h.bot.ProcessUpdate(ctx, convText("1-2"))

	require.Len(t, h.docs, 1)
	rows := resultButtons(t, h)
	require.Equal(t, "💾 Сохранить PDF", rows[0][0].Text, "a deleted source can't be replaced")
	require.True(t, strings.HasPrefix(rows[0][0].CallbackData, "conv:sv:"))
}

func TestConvertSave_pendingDocumentTakesTheFile(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	editsBefore := h.edits

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok)) // double tap

	require.Len(t, h.repo.Docs, 1, "no new document")
	got := h.repo.Docs[src.ID]
	require.Equal(t, "res-0", got.TgFileID)
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, docsbot.StatusPending, got.Status, "the category is still to be chosen")
	require.Equal(t, src.Title, got.Title)
	require.Equal(t, src.Category, got.Category)
	require.Equal(t, editsBefore+1, h.edits, "the buttons are removed once")
	require.Contains(t, h.allTexts(), "Подменил файл. Категорию выбери в сообщении выше.")
	require.Contains(t, h.lastText(), "Эта кнопка уже не работает", "the second tap is stale")
}

func TestConvertSave_documentSavedMeanwhileAsksBeforeReplacing(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	saved := h.repo.Docs[src.ID]
	saved.Status = docsbot.StatusSaved
	h.repo.Docs[src.ID] = saved

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))

	require.Equal(t, saved, h.repo.Docs[src.ID], "nothing is replaced silently")
	require.Contains(t, lastCaption(h), "Заменить оригинал этим файлом?")
	require.Contains(t, lastCaption(h), "conv:rpy:"+tok)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpn:"+tok))
	require.Contains(t, lastCaption(h), "conv:rp:"+tok, "cancel brings back the replace button")

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	require.Equal(t, "res-uniq-0", h.repo.Docs[src.ID].TgFileUniqueID, "confirmed")
}

func TestConvertSave_pendingDocumentGone(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	delete(h.repo.Docs, src.ID)
	editsBefore := h.edits

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
	require.Empty(t, h.repo.Docs, "nothing is created in its place")
	require.Equal(t, editsBefore+1, h.edits, "the dead buttons are removed")
}

func TestConvertSave_legacyButtonsAnswerStale(t *testing.T) {
	h := newConvHarness(t)
	tok := compressFromCommand(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	foreign := callbackUpdate("conv:save:" + tok)
	foreign.CallbackQuery.From.ID = testUser + 1

	for _, u := range []*models.Update{
		callbackUpdate("conv:save:" + tok), callbackUpdate("conv:save:deadbeef"),
		callbackUpdate("conv:save:"), callbackUpdate("conv:save"), foreign,
	} {
		require.NotPanics(t, func() { h.bot.ProcessUpdate(ctx, u) })
		require.Contains(t, h.lastText(), "Эта кнопка уже не работает")
	}

	require.Empty(t, h.repo.Docs, "an old button saves nothing")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))
	require.Len(t, h.repo.Docs, 1, "the live button of the same result still works")
}

func TestConvertSkip_removesButtonsAndKillsTheToken(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	editsBefore := h.edits

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))

	require.Equal(t, editsBefore+1, h.edits, "the buttons are removed")
	require.Equal(t, src, h.repo.Docs[src.ID], "the token no longer saves anything")
	require.Contains(t, h.lastText(), "Эта кнопка уже не работает")
}
