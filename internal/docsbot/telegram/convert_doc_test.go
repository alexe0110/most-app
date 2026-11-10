package telegram_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

var saveToken = regexp.MustCompile(`conv:save:([0-9a-f]+)`)

// seedSource stores the photo the converter downloads in these tests.
func seedSource(h *convHarness, status docsbot.Status) docsbot.Document {
	return h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: status, Kind: docsbot.KindPhoto, Category: docsbot.CatPassport,
		Title: "Паспорт", TgFileID: "src", TgFileUniqueID: "src-uniq",
	})
}

// compressFromMenu taps 🛠 and 🗜 1 МБ on the document and returns the save token.
func compressFromMenu(t *testing.T, h *convHarness, id docsbot.DocumentID) string {
	t.Helper()
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
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
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)

	require.Equal(t, "Паспорт-сжатый.jpg", h.docs[0].filename)
	require.Len(t, h.repo.docs, 1, "nothing is saved before the tap")

	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok)) // double tap

	require.Len(t, h.repo.docs, 2, "one new document, however many taps")
	var got docsbot.Document
	for id, d := range h.repo.docs {
		if id != src.ID {
			got = d
		}
	}
	require.Equal(t, "Паспорт (сжатый)", got.Title)
	require.Equal(t, docsbot.CatPassport, got.Category)
	require.Equal(t, docsbot.StatusSaved, got.Status)
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, src, h.repo.docs[src.ID], "the original is untouched")
}

func TestConvertDoc_skipSavesNothing(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	tok := compressFromMenu(t, h, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:skip:"+tok))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok)) // the choice is gone

	require.Len(t, h.repo.docs, 1)
	require.Equal(t, 1, h.edits, "buttons removed once")
}

func TestConvertDoc_pdfFromCard(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusSaved)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":pdf"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "Паспорт-PDF.pdf", h.docs[0].filename)
	require.True(t, strings.HasPrefix(string(h.docs[0].data), "%PDF"))
}

func TestConvertDoc_pendingSourceSavedInOther(t *testing.T) {
	h := newConvHarness(t)
	src := seedSource(h, docsbot.StatusPending)
	tok := compressFromMenu(t, h, src.ID)

	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok))

	require.Len(t, h.repo.docs, 2)
	for id, d := range h.repo.docs {
		if id != src.ID {
			require.Equal(t, docsbot.StatusSaved, d.Status, "no category question")
			require.Equal(t, docsbot.CatOther, d.Category)
			require.Equal(t, "Паспорт-сжатый.jpg", d.Title)
		}
	}
}

func TestConvertDoc_foreignDocumentRefused(t *testing.T) {
	h := newConvHarness(t)
	other := h.repo.seed(docsbot.Document{
		UserID: "someone-else", Status: docsbot.StatusSaved, Kind: docsbot.KindPhoto,
		Category: docsbot.CatOther, Title: "Чужое", TgFileID: "x", TgFileUniqueID: "x",
	})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(other.ID)+":c1"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "уже нет")
}

func TestConvertDoc_nonImageRefused(t *testing.T) {
	h := newConvHarness(t)
	pdf := h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "application/msword",
		Category: docsbot.CatOther, Title: "a.doc", TgFileID: "x", TgFileUniqueID: "x",
	})
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(pdf.ID)+":pdf"))

	require.Empty(t, h.docs)
}

func TestConvertDoc_menuCancelDeletesMenu(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:x"))

	require.Empty(t, h.docs)
}

func TestConvert_commandResultSavedOnlyOnTap(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:limit2"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:fmtjpeg"))
	require.Len(t, h.docs, 1)
	require.Empty(t, h.repo.docs)

	m := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+m[1]))

	require.Len(t, h.repo.docs, 1, "no source document: saved at once")
	for _, d := range h.repo.docs {
		require.Equal(t, docsbot.StatusSaved, d.Status)
		require.Equal(t, docsbot.CatOther, d.Category)
	}
}

func TestCard_convertRowOnlyForConvertible(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want bool
	}{
		{"photo", docsbot.Document{Kind: docsbot.KindPhoto}, true},
		{"png file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "image/png"}, true},
		{"pdf file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/pdf"}, true},
		{"word file", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/msword"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, sent, repo := newBot(t)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)
			tt.doc.UserID, tt.doc.Status, tt.doc.Category = testUserID, docsbot.StatusSaved, docsbot.CatOther
			tt.doc.Title, tt.doc.TgFileID, tt.doc.TgFileUniqueID = "Файл", "f", "u"
			doc := repo.seed(tt.doc)

			b.ProcessUpdate(ctx, dataCallback(100, "d:oc:"+string(doc.ID)+":s"))

			require.Len(t, sent.editMarkups, 1)
			require.Equal(t, tt.want, strings.Contains(sent.editMarkups[0], "conv:doc:"+string(doc.ID)), "%q", sent.editMarkups[0])
		})
	}
}

func TestReceive_photoPickerOffersConvert(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, photoUpdate(1))

	require.Len(t, repo.docs, 1)
	for id := range repo.docs {
		require.Contains(t, sent.markups[0], "conv:doc:"+string(id))
		require.Contains(t, sent.markups[0], "d:cat:"+string(id)+":passport", "category buttons stay")
	}
}
