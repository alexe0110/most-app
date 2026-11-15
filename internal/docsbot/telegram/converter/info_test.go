package converter_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

const (
	// infoDocID is a real-length document id: callback data must fit 64 bytes with it.
	infoDocID = docsbot.DocumentID("0b9f0c6e-4c1f-4f0e-9d3a-6c1d2f5a7b11")
	// pickerMsg is the message with the picker, infoMsg the info screen's.
	pickerMsg = 50
	infoMsg   = 51

	docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
)

var (
	keepToken  = regexp.MustCompile(`conv:kc:([0-9a-f]+)`)
	keepOrigRe = regexp.MustCompile(`conv:ko:([0-9a-f]+)`)
)

func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../platform/convert/testdata/" + name)
	require.NoError(t, err)
	return data
}

func infoCtx(t *testing.T) context.Context {
	t.Helper()
	return tg.ContextWithUserID(t.Context(), docsbottest.TestUserID)
}

// seedFile stores a file waiting for its category, as the intake leaves it.

func seedFile(h *convHarness, mime, title string, data []byte) {
	fileID := "file-" + title
	h.serve(fileID, data)
	h.repo.Seed(docsbot.Document{
		ID: infoDocID, UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindDocument,
		MIME: mime, Title: title, TgFileID: fileID, TgFileUniqueID: fileID + "-uniq", FileSize: int64(len(data)),
	})
}

// openInfo presses «О файле» on the picker.

func openInfo(t *testing.T, h *convHarness) {
	t.Helper()
	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:i:"+string(infoDocID), pickerMsg))
}

// cleanFile presses «Очистить» on the info screen and returns the keep token.

func cleanFile(t *testing.T, h *convHarness) string {
	t.Helper()
	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
	require.Len(t, h.docs, 1)
	m := keepToken.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m, "the copy offers to keep it: %q", h.docMarkups[0])
	return m[1]
}

func TestInfoFlow_showsWhatIsInsideEachFormat(t *testing.T) {
	t.Parallel()
	phone := []string{"Apple", "iPhone 15 Pro", "17.3.1", "19.02.2026 14:22", "55.75583, 37.61778",
		"maps.google.com/?q=55.755833,37.617778", "Открыть на карте", "Геолокация", "чувствительных"}
	tests := []struct {
		file, mime string
		format     docsbot.FileFormat
		want       []string
	}{
		{"gps.jpg", "image/jpeg", docsbot.FormatJPEG, append([]string{"JPG"}, phone...)},
		{"text.png", "image/png", docsbot.FormatPNG, append([]string{"PNG"}, phone...)},
		{"exif.webp", "image/webp", docsbot.FormatWebP, append([]string{"WEBP"}, phone...)},
		{"gps.heic", "image/heic", docsbot.FormatHEIC, append([]string{"HEIC"}, phone...)},
		{"word.pdf", "application/pdf", docsbot.FormatPDF, []string{"Ann Author", "Microsoft Word 2016", "PDF"}},
		{"author.docx", docxMIME, docsbot.FormatDOCX, []string{"Ann Author", "Bob Editor", "Acme Ltd", "Microsoft Office Word"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			h := newConvHarness(t)
			seedFile(h, tt.mime, "IMG_0001", fixtureFile(t, tt.file))

			openInfo(t, h)

			text := h.lastText()
			for _, want := range tt.want {
				require.Contains(t, text, want)
			}
			require.Contains(t, h.lastMarkup(), "conv:cl:"+string(infoDocID), "dirty file offers cleaning")
			require.Contains(t, h.lastMarkup(), "conv:x", "back")
			require.Equal(t, []docsbot.Event{{
				User: docsbottest.TestUserID, Name: docsbot.EventFileInfo, Props: docsbot.EventProps{Format: tt.format},
			}}, h.repo.Events, "the format is the only thing recorded")
		})
	}
}

func TestInfoFlow_compressedPhotoIsNotDownloaded(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	h.repo.Seed(docsbot.Document{
		ID: infoDocID, UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto,
		Title: "фото", TgFileID: "photo", TgFileUniqueID: "photo-uniq",
	})

	openInfo(t, h)

	require.Equal(t, converter.TextInfoCompressedPhoto, h.lastText())
	require.NotContains(t, h.lastMarkup(), "conv:cl:")
	require.Empty(t, h.downloads)
	require.Empty(t, h.repo.Events)
}

func TestInfoFlow_brokenFile(t *testing.T) {
	t.Parallel()
	broken := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, 0x7F, 0xFF}, []byte("PRIVATE-MARKER")...)
	tests := []struct {
		name, mime string
		data       []byte
		want       string
	}{
		{"truncated jpeg", "image/jpeg", broken, "Не получилось прочитать файл: он повреждён."},
		{"garbage pdf", "application/pdf", append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xAB}, 50)...), "Не получилось прочитать файл: он повреждён."},
		{"not a known format", "image/jpeg", []byte("just text"), "Этот формат не поддерживаю"},
		{"empty", "image/jpeg", nil, "Этот формат не поддерживаю"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			seedFile(h, tt.mime, "secret-name", tt.data)

			openInfo(t, h)
			require.Contains(t, h.lastText(), tt.want)
			require.Contains(t, h.lastMarkup(), "conv:x")
			require.NotContains(t, h.lastMarkup(), "conv:cl:")

			h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
			require.Contains(t, h.lastShown(), tt.want)
			require.Empty(t, h.docs, "no copy of a file that can't be read")
			require.Empty(t, h.repo.Events)
			require.NotContains(t, h.logs.String(), "PRIVATE-MARKER")
			require.NotContains(t, h.logs.String(), "secret-name")
		})
	}
}

func TestInfoFlow_downloadFailures(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.failDownloads = true

	openInfo(t, h)
	require.Equal(t, converter.TextConvertDownloadFailed, h.lastText())
	require.Contains(t, h.lastMarkup(), "conv:x")

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
	require.Contains(t, h.lastShown(), "Не удалось получить файл")
	require.Empty(t, h.docs)
	require.Empty(t, h.repo.Events)
}

func TestInfoFlow_tooLargeAndUnsupportedDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		doc  docsbot.Document
		want string
	}{
		{"over 20 MB", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/pdf", FileSize: 21 << 20}, "Файл больше 20\u00a0МБ"},
		{"video", docsbot.Document{Kind: docsbot.KindDocument, MIME: "video/mp4", FileSize: 1 << 20}, "Этот формат не поддерживаю"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newConvHarness(t)
			doc := tt.doc
			doc.ID, doc.UserID, doc.Status, doc.Title = infoDocID, docsbottest.TestUserID, docsbot.StatusPending, "big"
			doc.TgFileID, doc.TgFileUniqueID = "big", "big-uniq"
			h.repo.Seed(doc)

			openInfo(t, h)
			h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))

			require.Empty(t, h.downloads, "never fetched")
			require.Contains(t, h.allTexts(), tt.want)
			require.Empty(t, h.docs)
		})
	}
}

// customDOCX is a Word package with the given author and nothing else to show.

func customDOCX(t *testing.T, author string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range [][2]string{
		{"[Content_Types].xml", "<Types/>"},
		{"word/document.xml", "<w:document/>"},
		{"docProps/core.xml", `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator>` + author + `</dc:creator></cp:coreProperties>`},
	} {
		w, err := zw.Create(p[0])
		require.NoError(t, err)
		_, err = w.Write([]byte(p[1]))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestInfoFlow_valuesFromTheFileAreEscapedAndBounded(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	author := `&lt;b&gt;Ann&lt;/b&gt; &amp; &lt;a href="x"&gt;` + strings.Repeat("я", 400000)
	seedFile(h, docxMIME, strings.Repeat("Ж", 5000)+"<i>.docx", customDOCX(t, author))

	openInfo(t, h)

	text := h.lastText()
	require.Contains(t, text, "&lt;b&gt;Ann&lt;/b&gt; &amp; &lt;a href=")
	require.NotContains(t, text, "<b>Ann")
	require.NotContains(t, text, "<i>.docx")
	require.NotContains(t, text, `<a href="x"`)
	require.LessOrEqual(t, len([]rune(text)), 4096, "one Telegram message")
}

// pngWithText is text.png with n more tEXt chunks of long values after IHDR.

func pngWithText(t *testing.T, n int) []byte {
	t.Helper()
	src := fixtureFile(t, "text.png")
	const afterIHDR = 8 + 4 + 4 + 13 + 4
	out := bytes.Clone(src[:afterIHDR])
	for i := range n {
		body := []byte(fmt.Sprintf("Tag%03d\x00%s", i, strings.Repeat("v", 150)))
		chunk := append([]byte("tEXt"), body...)
		out = binary.BigEndian.AppendUint32(out, uint32(len(body)))
		out = append(out, chunk...)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
	}
	return append(out, src[afterIHDR:]...)
}

func TestInfoFlow_longListIsCutAndSentAsFile(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/png", "Скан/паспорта.png", pngWithText(t, 60))

	openInfo(t, h)

	require.LessOrEqual(t, len([]rune(h.lastText())), 4096)
	require.Contains(t, h.lastText(), "…и ещё")
	require.Len(t, h.docs, 1, "the whole list goes as a file")
	require.Equal(t, "metadata-Скан_паспорта.txt", h.docs[0].filename)
	require.Contains(t, string(h.docs[0].data), "PNG:Tag059: "+strings.Repeat("v", 150))
}

func TestInfoFlow_logsAndEventsCarryNoMetadata(t *testing.T) {
	t.Parallel()
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "Квартира-Иванова", fixtureFile(t, "gps.jpg"))

	openInfo(t, h)
	tok := cleanFile(t, h)
	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:kc:"+tok, 60))

	logs := h.logs.String()
	require.Contains(t, logs, "file inspected", "the test sees the log it checks")
	require.Contains(t, logs, "file cleaned")
	for _, secret := range []string{"iPhone", "Apple", "55.75", "37.61", "Ann Author", "Photoshop", "17.3.1", "Квартира", "Иванова", "IMG_"} {
		require.NotContains(t, logs, secret)
	}
	for _, e := range h.repo.Events {
		require.Equal(t, docsbot.EventProps{Format: docsbot.FormatJPEG}, e.Props)
	}
}

func TestInfoFlow_logsOfOtherFormatsCarryNoMetadata(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ file, mime string }{
		{"word.pdf", "application/pdf"},
		{"author.docx", docxMIME},
		{"gps.heic", "image/heic"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			h := newConvHarness(t)
			seedFile(h, tt.mime, "Договор-Петрова", fixtureFile(t, tt.file))

			openInfo(t, h)
			cleanFile(t, h)

			logs := h.logs.String()
			for _, secret := range []string{"iPhone", "Apple", "55.75", "Ann Author", "Bob Editor", "Acme", "Microsoft", "Договор", "Петрова"} {
				require.NotContains(t, logs, secret)
			}
		})
	}
}
