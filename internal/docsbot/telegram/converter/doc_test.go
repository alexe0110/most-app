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
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))

	require.Contains(t, h.lastText(), "уже нет")
	require.Empty(t, h.repo.Docs, "nothing was created")
}

func TestConvertQA_unknownOrGarbageTokens(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for _, data := range []string{"conv:sv:", "conv:sv:deadbeef", "conv:save:", "conv:save:deadbeef", "conv:skip:nope", "conv:alb:zzz"} {
		h.bot.ProcessUpdate(ctx, callbackUpdate(data))
	}

	require.Empty(t, h.repo.Docs)
	require.Empty(t, h.docs)
}

func TestConvertQA_saveHitsPendingLimit(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	tok := compressFromCommand(t, h)
	for i := 0; i < 20; i++ {
		h.repo.Seed(docsbot.Document{
			UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto,
			Title: "p", TgFileID: fmt.Sprintf("p%d", i), TgFileUniqueID: fmt.Sprintf("pu%d", i),
		})
	}
	before := len(h.repo.Docs)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))

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

// resultToken finds the token of a result's first button: save or replace.
var resultToken = regexp.MustCompile(`conv:(?:sv|rp):([0-9a-f]+)`)

// seedSource stores the photo the converter downloads in these tests; it is big
// enough for every compression preset to shrink it.
func seedSource(h *convHarness, status docsbot.Status) docsbot.Document {
	h.serveBig("src")
	return h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: status, Kind: docsbot.KindPhoto, Category: docsbot.CatPassport,
		Title: "Паспорт", TgFileID: "src", TgFileUniqueID: "src-uniq",
	})
}

// compressFromMenu taps 🛠 and 🗜 1 МБ on the document and returns the result's token.
func compressFromMenu(t *testing.T, h *convHarness, id docsbot.DocumentID) string {
	t.Helper()
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(id)))
	require.Contains(t, h.lastText(), "Что сделать")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(id)+":c1"))
	require.Len(t, h.docs, 1)
	m := resultToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m, "result carries the save or replace button: %q", h.docMarkups[0])
	require.Contains(t, h.docMarkups[0], "conv:skip:"+m[1])
	return m[1]
}

// compressFromCommand compresses a photo sent to /convert (no stored document) and
// returns the result's token.
func compressFromCommand(t *testing.T, h *convHarness) string {
	t.Helper()
	h.serveBig("large")
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))
	require.Len(t, h.docs, 1)
	m := resultToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m, "result carries the save button: %q", h.docMarkups[0])
	return m[1]
}

func TestConvertDoc_cardCompressNamesTheFileAndSavesNothing(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	compressFromMenu(t, h, src.ID)

	require.Equal(t, "Паспорт-сжатый.jpg", h.docs[0].filename)
	require.Regexp(t, `^Готово: .+ \(было .+\)\.$`, h.docs[0].caption, "a compressed result says how much smaller it got")
	require.Len(t, h.repo.Docs, 1, "nothing is saved before a tap")
	require.Equal(t, src, h.repo.Docs[src.ID])
}

func TestConvertDoc_skipSavesNothing(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok)) // the choice is gone
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:rpy:"+tok))

	require.Len(t, h.repo.Docs, 1)
	require.Equal(t, src, h.repo.Docs[src.ID], "nothing replaced")
	require.Equal(t, 1, h.edits, "buttons removed once")
	require.Contains(t, h.lastText(), converter.TextConvertResultOld)
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

func TestConvert_commandResultAsksCategoryOnTap(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	tok := compressFromCommand(t, h)
	require.Empty(t, h.repo.Docs, "nothing is stored before the tap")
	editsBefore := h.edits

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok)) // double tap

	require.Len(t, h.repo.Docs, 1, "one new document, however many taps")
	for _, d := range h.repo.Docs {
		require.Equal(t, docsbot.StatusPending, d.Status, "waits for its category")
		require.Equal(t, "res-uniq-0", d.TgFileUniqueID)
		require.Equal(t, "фото-сжатый", d.Title)
	}
	require.Contains(t, h.allTexts(), "В какую категорию сохранить?")
	require.Contains(t, strings.Join(h.markupBodies, "\n"), "d:cat:", "the usual category picker")
	require.Contains(t, h.lastText(), converter.TextConvertResultOld, "the second tap is stale")
	require.Equal(t, editsBefore+1, h.edits, "the buttons of the result are removed once")
}

func TestConvertDoc_strangerCannotSpendTheResult(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	tok := compressFromCommand(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	for _, data := range []string{"conv:sv:", "conv:save:", "conv:skip:", "conv:rp:", "conv:rpy:", "conv:rpn:"} {
		u := callbackUpdate(data + tok)
		u.CallbackQuery.From.ID = testUser + 1
		h.bot.ProcessUpdate(ctx, u)
	}

	require.Empty(t, h.repo.Docs, "nothing was saved or replaced")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))
	require.Len(t, h.repo.Docs, 1, "the owner's token survived the strangers")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:sv:"+tok))
	require.Len(t, h.repo.Docs, 1, "and acts once")
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
