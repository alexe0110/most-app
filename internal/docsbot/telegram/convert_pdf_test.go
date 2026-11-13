package telegram_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
	"github.com/go-telegram/bot/models"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvert_photosToPDF(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	for i := 1; i <= 3; i++ {
		h.bot.ProcessUpdate(ctx, photoUpdate(i))
	}
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	doc := h.docs[0]
	require.True(t, strings.HasPrefix(doc.filename, "Скан "), "got %q", doc.filename)
	require.True(t, strings.HasSuffix(doc.filename, ".pdf"))
	require.True(t, bytes.HasPrefix(doc.data, []byte("%PDF")))
	require.Contains(t, string(doc.data), "/Count 3", "one page per photo")
	require.Empty(t, h.repo.docs, "a result is not saved unless asked")
}

// The 21st photo is refused with a clear text; the session stays alive and the
// first twenty still become a PDF. Photos are not downloaded while collecting.
func TestConvert_photosToPDF_pageLimit(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	for i := 1; i <= 21; i++ {
		h.bot.ProcessUpdate(ctx, photoUpdate(i))
	}

	require.Empty(t, h.downloads, "photos are downloaded only when the PDF is built")
	require.Contains(t, h.allTexts(), "не больше 20 страниц")

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1, "the session survived the refusal")
	require.Contains(t, string(h.docs[0].data), "/Count 20")
	require.Len(t, h.downloads, 20)
}

func TestConvert_photosToPDF_sizeLimit(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	big := photoUpdate(1)
	big.Message.Photo[1].FileSize = 21 << 20
	h.bot.ProcessUpdate(ctx, big)
	h.bot.ProcessUpdate(ctx, photoUpdate(2))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Contains(t, h.allTexts(), "больше 20 МБ")
	require.Len(t, h.docs, 1)
	require.Contains(t, string(h.docs[0].data), "/Count 1", "only the photo that fit")
}

// Real sizes can differ from the declared ones: the PDF is refused after the
// download and the session ends instead of looping on the same photos.
func TestConvert_photosToPDF_realSizeOverLimitEndsSession(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()
	h.serve("large", make([]byte, 11<<20))

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, photoUpdate(2))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Contains(t, strings.Join(h.editTexts, "\n"), "больше 20 МБ", "the progress note turns into the refusal")
	require.Empty(t, h.docs)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Empty(t, h.docs, "the session is gone")
	require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
}

func testPDF(t *testing.T, pages int) []byte {
	t.Helper()
	p := fpdf.New("P", "mm", "A4", "")
	p.SetFont("Helvetica", "", 12)
	for range pages {
		p.AddPage()
		p.Cell(40, 10, "page")
	}
	var buf bytes.Buffer
	require.NoError(t, p.Output(&buf))
	return buf.Bytes()
}

func pdfPages(t *testing.T, data []byte) int {
	t.Helper()
	n, err := convert.PageCount(t.Context(), data)
	require.NoError(t, err)
	return n
}

// seedPDF stores a saved 3-page PDF "Договор.pdf" in certificates.
func seedPDF(t *testing.T, h *convHarness) docsbot.Document {
	t.Helper()
	h.serve("pdf-src", testPDF(t, 3))
	return h.repo.seed(docsbot.Document{
		UserID: testUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "application/pdf",
		Category: docsbot.CatCertificates, Title: "Договор.pdf", TgFileID: "pdf-src", TgFileUniqueID: "pdf-uniq",
	})
}

func pdfUpdate(fileID string, size int64) *models.Update {
	return &models.Update{Message: &models.Message{
		ID:       9,
		From:     &models.User{ID: testUser},
		Chat:     models.Chat{ID: testChat, Type: models.ChatTypePrivate},
		Document: &models.Document{FileID: fileID, FileUniqueID: "u-" + fileID, MimeType: "application/pdf", FileSize: size},
	}}
}

func TestConvertPDF_menuAndRotate(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:doc:"+string(src.ID)))
	for _, op := range []string{"rot", "keep", "drop", "merge"} {
		require.Contains(t, h.lastMarkup(), "conv:q:"+string(src.ID)+":"+op)
	}
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":rot"))

	require.Len(t, h.docs, 1)
	require.Equal(t, "Договор-изменён.pdf", h.docs[0].filename)
	rc, err := api.ReadContext(t.Context(), bytes.NewReader(h.docs[0].data), model.NewDefaultConfiguration())
	require.NoError(t, err)
	require.NoError(t, api.ValidateContext(t.Context(), rc))
	require.Equal(t, 3, rc.PageCount)
	_, _, inh, err := rc.PageDict(t.Context(), 1, false)
	require.NoError(t, err)
	require.Equal(t, 90, inh.Rotate)

	// 💾 saves a new document next to the original.
	tok := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, tok)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok[1]))
	require.Len(t, h.repo.docs, 2)
	for id, d := range h.repo.docs {
		if id != src.ID {
			require.Equal(t, "Договор (изменён)", d.Title)
			require.Equal(t, 3, d.PageCount, "the converter counts the pages of its PDF result")
			require.Equal(t, docsbot.CatCertificates, d.Category)
			require.Equal(t, docsbot.StatusSaved, d.Status)
		}
	}
}

func TestConvertPDF_keepAndDropPages(t *testing.T) {
	tests := []struct {
		op, sel string
		want    int
	}{
		{"keep", "1-2", 2},
		{"drop", "1", 2},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedPDF(t, h)
			ctx := tg.ContextWithUserID(t.Context(), testUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":"+tt.op))
			require.Contains(t, h.lastShown(), "Какие страницы")
			h.bot.ProcessUpdate(ctx, convText("abc"))
			require.Empty(t, h.docs)
			require.Contains(t, h.lastShown(), "Не понял")
			h.bot.ProcessUpdate(ctx, convText("7"))
			require.Empty(t, h.docs, "out of range: the session stays")
			h.bot.ProcessUpdate(ctx, convText(tt.sel))

			require.Len(t, h.docs, 1)
			require.Equal(t, tt.want, pdfPages(t, h.docs[0].data))
			require.Len(t, h.repo.docs, 1, "nothing saved without 💾")
		})
	}
}

func TestConvertPDF_dropAllPagesRefused(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":drop"))
	h.bot.ProcessUpdate(ctx, convText("1-3"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "Не понял")
}

func TestConvertPDF_merge(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("pdf-add", testPDF(t, 2))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "ещё хотя бы один")

	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-add", 1000))
	require.Contains(t, h.lastShown(), "Всего файлов: 2")
	require.Len(t, h.repo.docs, 1, "added PDFs are not stored")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	require.Equal(t, 5, pdfPages(t, h.docs[0].data))
	require.Equal(t, "Договор-изменён.pdf", h.docs[0].filename)
}

func TestConvertPDF_mergeRefusesOversizeAndGarbage(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("junk", []byte("not a pdf"))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	h.bot.ProcessUpdate(ctx, pdfUpdate("big", convert.MaxPDFBytes))
	require.Contains(t, h.lastShown(), "20 МБ")
	require.Empty(t, h.downloads[1:], "the declared size is checked before downloading")

	h.bot.ProcessUpdate(ctx, pdfUpdate("junk", 9))
	require.Contains(t, h.lastShown(), "Не получилось прочитать PDF")
}

func TestConvert_commandRefusesPDF(t *testing.T) {
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-x", 100))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "пока не умею")
}

func TestReceive_pdfPickerOffersActions(t *testing.T) {
	b, sent, repo := newBot(t)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	b.ProcessUpdate(ctx, pdfUpdate("pdf-x", 100))

	require.Len(t, repo.docs, 1)
	require.Contains(t, sent.markups[0], "conv:doc:")
	require.Contains(t, sent.markups[0], "Действия с PDF")
}

func TestConvertPDF_doubleDoneMergesOnce(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("pdf-add", testPDF(t, 1))
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))
	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-add", 100))

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
}

func TestConvertPDF_secondRangeTextRunsOnce(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))

	h.bot.ProcessUpdate(ctx, convText("1"))
	h.bot.ProcessUpdate(ctx, convText("2"))

	require.Len(t, h.docs, 1)
}

func TestConvertPDF_mergeRefusesNonPDF(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	require.Contains(t, h.lastShown(), "Это не PDF")
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("x", "image/png", "a.png"))
	require.Contains(t, h.lastShown(), "Это не PDF")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Contains(t, h.lastShown(), "ещё хотя бы один", "the session survived")
}

func TestConvertPDF_newOperationCancelsLiveSession(t *testing.T) {
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	require.Contains(t, h.allTexts(), "Предыдущее преобразование отменено")
	require.Contains(t, h.lastShown(), "Пришли PDF")
}

func TestConvert_resultWithoutStoredFileLosesButtons(t *testing.T) {
	h := newConvHarness(t)
	h.noDocument = true
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), testUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":rot"))

	require.Len(t, h.docs, 1)
	require.Equal(t, 1, h.edits, "the dead buttons are removed")
}
