package converter_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvertQA_saveAfterSourceDeleted(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	delete(h.repo.Docs, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
	require.Empty(t, h.repo.Docs, "nothing was created")
}

func TestConvertQA_unknownOrGarbageTokens(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for _, data := range []string{"conv:save:", "conv:save:deadbeef", "conv:skip:nope", "conv:alb:zzz"} {
		h.bot.ProcessUpdate(ctx, callbackUpdate(data))
	}

	require.Empty(t, h.repo.Docs)
	require.Empty(t, h.docs)
}

func TestConvertQA_saveHitsPendingLimit(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	for i := 0; i < 20; i++ {
		h.repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto,
			Title: "p", TgFileID: fmt.Sprintf("p%d", i), TgFileUniqueID: fmt.Sprintf("pu%d", i),
		})
	}
	before := len(h.repo.Docs)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Len(t, h.repo.Docs, before, "nothing added past the limit")
	require.Contains(t, h.lastText(), "категорию")
}

func TestConvertQA_quickOnMalformedData(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for _, data := range []string{"conv:q:", "conv:q:abc", "conv:q::c1", "conv:doc:", "conv:q:abc:zzz"} {
		h.bot.ProcessUpdate(ctx, callbackUpdate(data))
	}
	require.Empty(t, h.docs)
	require.Empty(t, h.repo.Docs)
}

var saveToken = regexp.MustCompile(`conv:save:([0-9a-f]+)`)

// seedSource stores the photo the converter downloads in these tests.
func seedSource(h *convHarness, status docsbot.Status) docsbot.Document {
	return h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: status, Kind: docsbot.KindPhoto, Category: docsbot.CatPassport,
		Title: "Паспорт", TgFileID: "src", TgFileUniqueID: "src-uniq",
	})
}

// compressFromMenu taps 🛠 and 🗜 1 МБ on the document and returns the save token.
func compressFromMenu(t *testing.T, h *convHarness, id docsbot.DocumentID) string {
	t.Helper()
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(id)))
	require.Contains(t, h.lastText(), "Что сделать")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(id)+":c1"))
	require.Len(t, h.docs, 1)
	m := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m, "result carries the save button: %q", h.docMarkups[0])
	require.Contains(t, h.docMarkups[0], "conv:skip:"+m[1])
	return m[1]
}

func TestConvertDoc_cardCompressThenSave(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)

	require.Equal(t, "Паспорт-сжатый.jpg", h.docs[0].filename)
	require.Regexp(t, `^Готово: .+ \(было .+\)\.$`, h.docs[0].caption, "a compressed result says how much smaller it got")
	require.Len(t, h.repo.Docs, 1, "nothing is saved before the tap")

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok)) // double tap

	require.Len(t, h.repo.Docs, 2, "one new document, however many taps")
	var got docsbot.Document
	for id, d := range h.repo.Docs {
		if id != src.ID {
			got = d
		}
	}
	require.Equal(t, "Паспорт (сжатый)", got.Title)
	require.Equal(t, docsbot.CatPassport, got.Category)
	require.Equal(t, docsbot.StatusSaved, got.Status)
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, src, h.repo.Docs[src.ID], "the original is untouched")
}

func TestConvertDoc_skipSavesNothing(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok)) // the choice is gone

	require.Len(t, h.repo.Docs, 1)
	require.Equal(t, 1, h.edits, "buttons removed once")
}

func TestConvertDoc_pdfFromCard(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":pdf"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "Паспорт-PDF.pdf", h.docs[0].filename)
	require.True(t, strings.HasPrefix(string(h.docs[0].data), "%PDF"))
	require.Equal(t, fmt.Sprintf(converter.TextResultPages, 1, converter.PluralPageOne), h.docs[0].caption)
}

func TestConvertDoc_webpToJPGHasNoCaption(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.serve("webp-src", tinyWebP(t))
	src := h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "image/webp",
		Category: docsbot.CatOther, Title: "Скан.webp", TgFileID: "webp-src", TgFileUniqueID: "webp-uniq",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":jpg"))

	require.Len(t, h.docs, 1)
	require.Empty(t, h.docs[0].caption, "only compressed images and PDFs get a caption")
}

func TestConvertDoc_pendingSourceSavedInOther(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Len(t, h.repo.Docs, 2)
	for id, d := range h.repo.Docs {
		if id != src.ID {
			require.Equal(t, docsbot.StatusSaved, d.Status, "no category question")
			require.Equal(t, docsbot.CatOther, d.Category)
			require.Equal(t, "Паспорт-сжатый", d.Title, "the title carries no extension")
		}
	}
}

func TestConvertDoc_foreignDocumentRefused(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	other := h.repo.Seed(docsbot.Document{
		UserID: "someone-else", Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto,
		Category: docsbot.CatOther, Title: "Чужое", TgFileID: "x", TgFileUniqueID: "x",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(other.ID)+":c1"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "уже нет")
}

func TestConvertDoc_nonImageRefused(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	pdf := h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "application/msword",
		Category: docsbot.CatOther, Title: "a.doc", TgFileID: "x", TgFileUniqueID: "x",
	})
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(pdf.ID)+":pdf"))

	require.Empty(t, h.docs)
}

func TestConvertDoc_menuCancelDeletesMenu(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:x"))

	require.Empty(t, h.docs)
}

func TestConvert_commandResultSavedOnlyOnTap(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))
	require.Len(t, h.docs, 1)
	require.Empty(t, h.repo.Docs)

	m := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+m[1]))

	require.Len(t, h.repo.Docs, 1, "no source document: saved at once")
	for _, d := range h.repo.Docs {
		require.Equal(t, docsbot.StatusSaved, d.Status)
		require.Equal(t, docsbot.CatOther, d.Category)
	}
}

func TestConvertDoc_strangerCannotSpendTheResult(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	stored := len(h.repo.Docs)

	for _, data := range []string{"conv:save:", "conv:skip:", "conv:rp:", "conv:rpy:", "conv:rpn:"} {
		u := callbackUpdate(data + tok)
		u.CallbackQuery.From.ID = testUser + 1
		h.bot.ProcessUpdate(ctx, u)
	}

	require.Len(t, h.repo.Docs, stored, "nothing was saved or replaced")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))
	require.Len(t, h.repo.Docs, stored+1, "the owner's token survived the strangers")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))
	require.Len(t, h.repo.Docs, stored+1, "and acts once")
}

// The stored document is fetched when the operation runs; if it can't be (deleted
// from Telegram, over the limit, timeout) the user sees the failure on the menu
// and can pick another operation.
func TestConvertDoc_quickDownloadFailureKeepsTheMenu(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{"file is gone", errors.New("get file: Bad Request: wrong file_id")},
		{"file too large", fmt.Errorf("download: %w", tg.ErrFileTooLarge)},
		{"timeout", fmt.Errorf("get file: %w", context.DeadlineExceeded)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedSource(h, docsbot.StatusSaved)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
			h.mu.Lock()
			h.downloadErr = tt.err
			h.mu.Unlock()

			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":c1"))

			require.Empty(t, h.docs)
			require.Contains(t, h.lastShown(), "Не удалось получить файл")
			h.mu.Lock()
			last := h.editMarkups[len(h.editMarkups)-1]
			h.mu.Unlock()
			require.Contains(t, last, "conv:q:"+string(src.ID)+":c1", "the menu is back")
		})
	}
}
