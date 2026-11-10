package telegram_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
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

func TestConvertReplace_keyboard(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	resultFromCard(t, h, src.ID)

	require.Contains(t, h.docMarkups[0], "Сохранить как новый")
	require.Contains(t, h.docMarkups[0], "Заменить оригинал")
	require.Contains(t, h.docMarkups[0], "Только файл")
	require.Contains(t, h.docMarkups[0], "conv:save:")
	require.Contains(t, h.docMarkups[0], "conv:skip:")
}

func TestConvertReplace_commandResultKeyboardUnchanged(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))

	require.Len(t, h.docMarkups, 1)
	require.Contains(t, h.docMarkups[0], "Сохранить в документы")
	require.Contains(t, h.docMarkups[0], "Только файл")
	require.NotContains(t, h.docMarkups[0], "conv:rp:")
	require.NotContains(t, h.docMarkups[0], "Заменить")
}

func TestConvertReplace_askThenConfirm(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok))

	require.Contains(t, lastCaption(h), "Исходный файл будет заменён")
	require.Contains(t, lastCaption(h), "conv:rpy:"+tok)
	require.Contains(t, lastCaption(h), "conv:rpn:"+tok)
	require.Equal(t, src, h.repo.docs[src.ID], "asking changes nothing")

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok)) // double tap

	got := h.repo.docs[src.ID]
	require.Len(t, h.repo.docs, 1, "no new document")
	require.Equal(t, "res-0", got.TgFileID)
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, src.Title, got.Title)
	require.Equal(t, src.Category, got.Category)
	require.Equal(t, src.MetaVersion+1, got.MetaVersion, "replaced once, however many taps")
	require.Contains(t, h.lastText(), "Эта кнопка устарела", "the second tap is stale")
}

func TestConvertReplace_cancelRestoresButtons(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpn:"+tok))

	require.Contains(t, lastCaption(h), "Заменить оригинал")
	require.Contains(t, lastCaption(h), "conv:save:"+tok)
	require.Equal(t, src, h.repo.docs[src.ID])

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rp:"+tok)) // the choice is still alive
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
	require.Equal(t, "res-uniq-0", h.repo.docs[src.ID].TgFileUniqueID)
}

func TestConvertReplace_staleToken(t *testing.T) {
	for _, data := range []string{"conv:rp:", "conv:rpy:", "conv:rpn:"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedSource(h, docsbot.StatusSaved)
			resultFromCard(t, h, src.ID)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate(data+"deadbeef"))

			require.Contains(t, h.lastText(), "Эта кнопка устарела")
			require.Equal(t, src, h.repo.docs[src.ID])
		})
	}
}

func TestConvertReplace_afterSkipIsStale(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Contains(t, h.lastText(), "Эта кнопка устарела")
	require.Equal(t, src, h.repo.docs[src.ID])
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
		}, "Выгрузить заново"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedSource(h, docsbot.StatusSaved)
			tok := resultFromCard(t, h, src.ID)
			d := h.repo.docs[src.ID]
			tt.mod(&d)
			h.repo.docs[src.ID] = d
			ctx := tg.ContextWithUserID(t.Context(), testUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

			require.Contains(t, h.lastText(), tt.want)
			require.Equal(t, d, h.repo.docs[src.ID], "nothing changed")
			require.Contains(t, lastCaption(h), "conv:save:"+tok, "buttons are back")

			// The user can retry once the document is free again.
			d.BackupStatus, d.SyncStatus = docsbot.BackupBackedUp, docsbot.SyncOK
			h.repo.docs[src.ID] = d
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))
			require.Equal(t, "res-uniq-0", h.repo.docs[src.ID].TgFileUniqueID)
		})
	}
}

func TestConvertReplace_duplicateFileShowsExistingCard(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, Category: docsbot.CatOther,
		Title: "Уже есть", TgFileID: "x", TgFileUniqueID: "res-uniq-0",
	})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Equal(t, src, h.repo.docs[src.ID])
	require.Contains(t, h.allTexts(), "Уже есть")
}

func TestConvertReplace_goneDocument(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := resultFromCard(t, h, src.ID)
	delete(h.repo.docs, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
}
