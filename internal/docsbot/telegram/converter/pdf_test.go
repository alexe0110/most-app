package converter_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-pdf/fpdf"
	"github.com/go-telegram/bot/models"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestConvert_photosToPDF(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
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
	require.Equal(t, fmt.Sprintf(converter.TextResultPages, 3, converter.PluralPageFew), doc.caption)
	require.Empty(t, h.repo.Docs, "a result is not saved unless asked")
}

func TestConvert_photosToPDF_captionAgreesWithThePageCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		photos int
		want   string
	}{
		{1, "Готово: 1 страница."},
		{2, "Готово: 2 страницы."},
		{5, "Готово: 5 страниц."},
		{11, "Готово: 11 страниц."},
		{20, "Готово: 20 страниц."},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			h := newConvHarness(t)
			ctx := t.Context()

			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
			for i := 1; i <= tt.photos; i++ {
				h.bot.ProcessUpdate(ctx, photoUpdate(i))
			}
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

			require.Len(t, h.docs, 1)
			require.Equal(t, tt.want, h.docs[0].caption)
		})
	}
}

// The 21st photo is refused with a clear text; the session stays alive and the
// first twenty still become a PDF. Photos are not downloaded while collecting.
func TestConvert_photosToPDF_pageLimit(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
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
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	big := photoUpdate(1)
	big.Message.Photo[1].FileSize = 21 << 20
	h.bot.ProcessUpdate(ctx, big)
	h.bot.ProcessUpdate(ctx, photoUpdate(2))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Contains(t, h.allTexts(), "больше 20\u00a0МБ")
	require.Len(t, h.docs, 1)
	require.Contains(t, string(h.docs[0].data), "/Count 1", "only the photo that fit")
}

// Real sizes can differ from the declared ones: the PDF is refused after the
// download and the session ends instead of looping on the same photos.
func TestConvert_photosToPDF_realSizeOverLimitEndsSession(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := t.Context()
	h.serve("large", make([]byte, 11<<20))

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	h.bot.ProcessUpdate(ctx, photoUpdate(2))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Contains(t, strings.Join(h.editTexts, "\n"), "больше 20\u00a0МБ", "the progress note turns into the refusal")
	require.Empty(t, h.docs)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Empty(t, h.docs, "the session is gone")
	require.Equal(t, converter.TextStale, h.lastText())
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
	return h.repo.Seed(docsbot.Document{
		UserID: docsbottest.TestUserID, Status: docsbot.StatusSaved, Kind: docsbot.KindDocument, MIME: "application/pdf",
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
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

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
	require.Equal(t, fmt.Sprintf(converter.TextResultPages, 3, converter.PluralPageFew), h.docs[0].caption)

	// 💾 saves a new document next to the original.
	tok := saveToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, tok)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:save:"+tok[1]))
	require.Len(t, h.repo.Docs, 2)
	for id, d := range h.repo.Docs {
		if id != src.ID {
			require.Equal(t, "Договор (изменён)", d.Title)
			require.Equal(t, 3, d.PageCount, "the converter counts the pages of its PDF result")
			require.Equal(t, docsbot.CatCertificates, d.Category)
			require.Equal(t, docsbot.StatusSaved, d.Status)
		}
	}
}

func TestConvertPDF_keepAndDropPages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		op, sel string
		prompt  string
		want    int
		caption string
	}{
		{"keep", "1-2", "Какие страницы оставить? Например: 1-3, 5", 2, "Готово: 2 страницы."},
		{"drop", "1", "Какие страницы убрать? Например: 1-3, 5", 2, "Готово: 2 страницы."},
		{"keep", "2", "Какие страницы оставить? Например: 1-3, 5", 1, "Готово: 1 страница."},
		{"drop", "2-3", "Какие страницы убрать? Например: 1-3, 5", 1, "Готово: 1 страница."},
	}
	for _, tt := range tests {
		t.Run(tt.op+" "+tt.sel, func(t *testing.T) {
			h := newConvHarness(t)
			src := seedPDF(t, h)
			ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":"+tt.op))
			require.Equal(t, tt.prompt, h.lastShown(), "keep and drop ask different questions")
			h.bot.ProcessUpdate(ctx, convText("abc"))
			require.Empty(t, h.docs)
			require.Contains(t, h.lastShown(), "Не понял")
			h.bot.ProcessUpdate(ctx, convText("7"))
			require.Empty(t, h.docs, "out of range: the session stays")
			h.bot.ProcessUpdate(ctx, convText(tt.sel))

			require.Len(t, h.docs, 1)
			require.Equal(t, tt.want, pdfPages(t, h.docs[0].data))
			require.Equal(t, tt.caption, h.docs[0].caption)
			require.Len(t, h.repo.Docs, 1, "nothing saved without 💾")
		})
	}
}

func TestConvertPDF_dropAllPagesRefused(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":drop"))
	h.bot.ProcessUpdate(ctx, convText("1-3"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "Не понял")
}

func TestConvertPDF_merge(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("pdf-add", testPDF(t, 2))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "ещё хотя бы один")

	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-add", 1000))
	require.Contains(t, h.lastShown(), "Уже 2 файла")
	require.Len(t, h.repo.Docs, 1, "added PDFs are not stored")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
	require.Equal(t, 5, pdfPages(t, h.docs[0].data))
	require.Equal(t, fmt.Sprintf(converter.TextResultPages, 5, converter.PluralPageMany), h.docs[0].caption)
	require.Equal(t, "Договор-изменён.pdf", h.docs[0].filename)
}

func TestConvertPDF_mergeRefusesOversizeAndGarbage(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("junk", []byte("not a pdf"))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	h.bot.ProcessUpdate(ctx, pdfUpdate("big", convert.MaxInputBytes))
	require.Contains(t, h.lastShown(), "20\u00a0МБ")
	require.Empty(t, h.downloads[1:], "the declared size is checked before downloading")

	h.bot.ProcessUpdate(ctx, pdfUpdate("junk", 9))
	require.Contains(t, h.lastShown(), "Не получилось открыть PDF")
}

func TestConvert_commandRefusesPDF(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-x", 100))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastShown(), "С таким файлом не справлюсь")
}

func TestConvertPDF_doubleDoneMergesOnce(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	h.serve("pdf-add", testPDF(t, 1))
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))
	h.bot.ProcessUpdate(ctx, pdfUpdate("pdf-add", 100))

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

	require.Len(t, h.docs, 1)
}

func TestConvertPDF_secondRangeTextRunsOnce(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))

	h.bot.ProcessUpdate(ctx, convText("1"))
	h.bot.ProcessUpdate(ctx, convText("2"))

	require.Len(t, h.docs, 1)
}

func TestConvertPDF_mergeRefusesNonPDF(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	h.bot.ProcessUpdate(ctx, photoUpdate(1))
	require.Contains(t, h.lastShown(), "Это не PDF")
	h.bot.ProcessUpdate(ctx, namedDocumentUpdate("x", "image/png", "a.png"))
	require.Contains(t, h.lastShown(), "Это не PDF")
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))
	require.Contains(t, h.lastShown(), "ещё хотя бы один", "the session survived")
}

func TestConvertPDF_newOperationCancelsLiveSession(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":keep"))

	h.mu.Lock()
	editsBefore, eventsBefore := h.edits, len(h.events)
	h.mu.Unlock()

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":merge"))

	require.Contains(t, h.allTexts(), "Предыдущее преобразование отменено")
	require.Contains(t, h.lastShown(), "Пришли PDF")
	h.mu.Lock()
	defer h.mu.Unlock()
	require.Equal(t, editsBefore+1, h.edits, "the old dialogue message loses its buttons")
	require.Len(t, h.events[eventsBefore:], 3)
	require.Contains(t, h.events[eventsBefore], "Обрабатываю", "progress note first")
	require.Equal(t, "send:Предыдущее преобразование отменено.", h.events[eventsBefore+1])
	require.Contains(t, h.events[eventsBefore+2], "Пришли PDF")
}

func TestConvert_resultWithoutStoredFileLosesButtons(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.noDocument = true
	src := seedPDF(t, h)
	ctx := tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)

	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:q:"+string(src.ID)+":rot"))

	require.Len(t, h.docs, 1)
	require.Equal(t, 1, h.edits, "the dead buttons are removed")
}

// The photos are fetched when the PDF is built. A failure on any of them (gone,
// too large for the Bot API, slow) keeps the collected pages so "done" can be
// tapped again.
func TestConvert_photosToPDF_downloadFailureKeepsThePages(t *testing.T) {
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
			ctx := t.Context()
			h.bot.ProcessUpdate(ctx, commandUpdate("convert"))
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:pdf"))
			h.bot.ProcessUpdate(ctx, photoUpdate(1))
			h.bot.ProcessUpdate(ctx, photoUpdate(2))

			h.mu.Lock()
			h.downloadErr = tt.err
			h.mu.Unlock()
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

			require.Empty(t, h.docs)
			require.Contains(t, h.lastShown(), "Не удалось получить файл")

			h.mu.Lock()
			h.downloadErr = nil
			h.mu.Unlock()
			h.bot.ProcessUpdate(ctx, callbackUpdate("conv:done"))

			require.Len(t, h.docs, 1)
			require.Contains(t, string(h.docs[0].data), "/Count 2", "both pages survived the failure")
		})
	}
}
