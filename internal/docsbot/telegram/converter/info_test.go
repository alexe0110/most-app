package converter_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
	"github.com/alexe0110/most-app/internal/platform/convert"
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
	phone := []string{"Apple", "iPhone 15 Pro", "17.3.1", "19.02.2026 14:22", "55.75583, 37.61778",
		"maps.google.com/?q=55.755833,37.617778", "Открыть на карте", "Видно, где сделан снимок"}
	tests := []struct {
		file, mime string
		format     docsbot.FileFormat
		want       []string
	}{
		{"gps.jpg", "image/jpeg", docsbot.FormatJPEG, append([]string{"JPEG", "EXIF, XMP"}, phone...)},
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
	h := newConvHarness(t)
	h.repo.Seed(docsbot.Document{
		ID: infoDocID, UserID: docsbottest.TestUserID, Status: docsbot.StatusPending, Kind: docsbot.KindPhoto,
		Title: "фото", TgFileID: "photo", TgFileUniqueID: "photo-uniq",
	})

	openInfo(t, h)

	require.Equal(t, "Telegram уже убрал метаданные. Чтобы проверить оригинал, отправь как файл", h.lastText())
	require.NotContains(t, h.lastMarkup(), "conv:cl:")
	require.Empty(t, h.downloads)
	require.Empty(t, h.repo.Events)
}

func TestInfoFlow_cleanFileOffersNothingToClean(t *testing.T) {
	clean, err := convert.Strip(t.Context(), fixtureFile(t, "gps.jpg"))
	require.NoError(t, err)
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "IMG_0001", clean)

	openInfo(t, h)

	require.Contains(t, h.lastText(), "Метаданных нет — файл чистый")
	require.NotContains(t, h.lastText(), "Очистить")
	require.NotContains(t, h.lastMarkup(), "conv:cl:")
	require.Contains(t, h.lastMarkup(), "conv:x")
}

func TestInfoFlow_cleanSendsACopyWithKeepButtons(t *testing.T) {
	h := newConvHarness(t)
	src := fixtureFile(t, "gps.jpg")
	seedFile(h, "image/jpeg", "Квартира", src)

	cleanFile(t, h)

	sent := h.docs[0]
	require.Equal(t, "Квартира-очищен.jpg", sent.filename)
	require.Contains(t, sent.caption, "Очищенная копия")
	require.Contains(t, sent.caption, "EXIF")
	after, err := convert.Inspect(t.Context(), sent.data)
	require.NoError(t, err)
	require.True(t, after.Clean())
	require.Contains(t, h.docMarkups[0], "Сохранить очищенный")
	require.Contains(t, h.docMarkups[0], "Сохранить оригинал")
	require.Contains(t, h.editTexts[0], "Обрабатываю", "the info screen becomes the progress message")
	require.NotContains(t, h.editMarkups[0], "conv:", "with no buttons while it works")
	require.Contains(t, h.deleted, infoMsg, "and is removed at the end")
	require.Equal(t, []docsbot.Event{{
		User: docsbottest.TestUserID, Name: docsbot.EventFileCleaned, Props: docsbot.EventProps{Format: docsbot.FormatJPEG},
	}}, h.repo.Events)
	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID, "nothing is replaced before the choice")
}

func TestInfoFlow_everyButtonFitsTelegramLimit(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	openInfo(t, h)
	cleanFile(t, h)

	h.mu.Lock()
	defer h.mu.Unlock()
	checked := 0
	for _, markup := range append(append([]string{}, h.markupBodies...), h.docMarkups...) {
		if markup == "" {
			continue
		}
		var kb struct {
			Rows [][]struct {
				Data string `json:"callback_data"`
			} `json:"inline_keyboard"`
		}
		require.NoError(t, json.Unmarshal([]byte(markup), &kb))
		for _, row := range kb.Rows {
			for _, b := range row {
				require.LessOrEqual(t, len(b.Data), 64, b.Data)
				checked++
			}
		}
	}
	require.GreaterOrEqual(t, checked, 4)
}

func TestInfoFlow_keepCleanedReplacesTheFileThenCategorySavesIt(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "Квартира", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	ctx := infoCtx(t)
	version := h.repo.Docs[infoDocID].MetaVersion

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60))

	got := h.repo.Docs[infoDocID]
	require.Equal(t, "res-0", got.TgFileID, "the cleaned copy that Telegram stored")
	require.Equal(t, "res-uniq-0", got.TgFileUniqueID)
	require.Equal(t, docsbot.StatusPending, got.Status)
	require.Equal(t, version+1, got.MetaVersion)
	require.Equal(t, "Выбери категорию:", h.lastText())
	require.Contains(t, h.lastMarkup(), "d:cat:"+string(infoDocID))

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60)) // double tap

	require.Equal(t, version+1, h.repo.Docs[infoDocID].MetaVersion, "replaced once")
	require.Equal(t, "Эта кнопка устарела. Преобразуй файл ещё раз.", h.lastText())

	h.bot.ProcessUpdate(ctx, callbackOn("d:cat:"+string(infoDocID)+":"+string(docsbot.CatPassport), 61))

	saved := h.repo.Docs[infoDocID]
	require.Equal(t, docsbot.StatusSaved, saved.Status)
	require.Equal(t, docsbot.CatPassport, saved.Category)
	require.Equal(t, "res-0", saved.TgFileID, "the cleaned copy is what is saved")
}

func TestInfoFlow_keepOriginalLeavesTheFile(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "Квартира", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	m := keepOrigRe.FindStringSubmatch(h.docMarkups[0])
	require.NotNil(t, m)
	require.Equal(t, tok, m[1])
	ctx := infoCtx(t)

	h.bot.ProcessUpdate(ctx, callbackOn("conv:ko:"+tok, 60))

	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID)
	require.Equal(t, "Выбери категорию:", h.lastShown())

	h.bot.ProcessUpdate(ctx, callbackOn("conv:kc:"+tok, 60))

	require.Equal(t, "file-Квартира", h.repo.Docs[infoDocID].TgFileID, "the choice is made once")
	require.Equal(t, "Эта кнопка устарела. Преобразуй файл ещё раз.", h.lastText())
}

func TestInfoFlow_keepCleanedOfADeletedDocument(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	delete(h.repo.Docs, infoDocID)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:kc:"+tok, 60))

	require.Equal(t, "Этого документа уже нет.", h.lastText())
	require.Empty(t, h.repo.Docs, "nothing is resurrected")
}

func TestInfoFlow_someoneElsesDocumentAndTokens(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	downloads := len(h.downloads)
	stranger := tg.ContextWithUserID(t.Context(), "22222222-2222-2222-2222-222222222222")
	press := func(data string, msgID int) {
		u := callbackOn(data, msgID)
		u.CallbackQuery.From.ID = 99
		h.bot.ProcessUpdate(stranger, u)
	}

	press("conv:i:"+string(infoDocID), 70)
	require.Equal(t, "Этого документа уже нет.", h.lastText())
	press("conv:cl:"+string(infoDocID), 71)
	require.Equal(t, "Этого документа уже нет.", h.lastText())
	press("conv:kc:"+tok, 72)
	require.Equal(t, "Эта кнопка устарела. Преобразуй файл ещё раз.", h.lastText(), "a token belongs to the user it was made for")
	press("conv:ko:"+tok, 73)

	require.Equal(t, downloads, len(h.downloads), "a stranger can't make the bot read the file")
	require.Len(t, h.docs, 1, "and gets no copy")
	require.Equal(t, "file-a", h.repo.Docs[infoDocID].TgFileID)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:kc:"+tok, 60))
	require.Equal(t, "res-0", h.repo.Docs[infoDocID].TgFileID, "the owner's token still works")
}

func TestInfoFlow_brokenFile(t *testing.T) {
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
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.failDownloads = true

	openInfo(t, h)
	require.Equal(t, "Не удалось получить файл. Попробуй ещё раз.", h.lastText())
	require.Contains(t, h.lastMarkup(), "conv:x")

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
	require.Contains(t, h.lastShown(), "Не удалось получить файл")
	require.Empty(t, h.docs)
	require.Empty(t, h.repo.Events)
}

func TestInfoFlow_sendFailureKeepsTheCleanButton(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.failDocs = true

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))

	require.Contains(t, h.lastShown(), "Не удалось отправить файл")
	require.Contains(t, h.editMarkups[len(h.editMarkups)-1], "conv:cl:", "retry is one tap away")
	require.Empty(t, h.repo.Events, "a copy that was not delivered is not counted")
}

func TestInfoFlow_tooLargeAndUnsupportedDocuments(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want string
	}{
		{"over 20 MB", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/pdf", FileSize: 21 << 20}, "Файл больше 20 МБ"},
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
	h := newConvHarness(t)
	author := `&lt;b&gt;Ann&lt;/b&gt; &amp; &lt;a href="x"&gt;` + strings.Repeat("я", 400000)
	seedFile(h, docxMIME, strings.Repeat("Ж", 5000)+"<i>.docx", customDOCX(t, author))

	openInfo(t, h)

	text := h.lastText()
	require.Contains(t, text, "&lt;b&gt;Ann&lt;/b&gt; &amp; &lt;a href=")
	require.NotContains(t, text, "<b>Ann")
	require.NotContains(t, text, "<i>")
	require.LessOrEqual(t, len([]rune(text)), 4096, "one Telegram message")
}

func TestInfoFlow_logsAndEventsCarryNoMetadata(t *testing.T) {
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

func TestInfoFlow_doubleTapOnCleanSendsOneCopy(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	h.mu.Lock()
	h.gate = make(chan struct{})
	gate := h.gate
	h.mu.Unlock()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg))
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.downloads) > 0
	}, 5*time.Second, 5*time.Millisecond)

	h.bot.ProcessUpdate(infoCtx(t), callbackOn("conv:cl:"+string(infoDocID), infoMsg)) // second tap while the first is busy
	close(gate)
	wg.Wait()

	require.Len(t, h.docs, 1, "one copy however many taps")
	require.Len(t, h.repo.Events, 1)
}

func TestInfoFlow_concurrentTapsOnTheKeepButtons(t *testing.T) {
	h := newConvHarness(t)
	seedFile(h, "image/jpeg", "a", fixtureFile(t, "gps.jpg"))
	tok := cleanFile(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := "conv:kc:" + tok
			if i%2 == 1 {
				data = "conv:ko:" + tok
			}
			h.bot.ProcessUpdate(infoCtx(t), callbackOn(data, 60+i))
		}()
	}
	wg.Wait()

	got := h.repo.Docs[infoDocID].TgFileID
	require.Contains(t, []string{"file-a", "res-0"}, got, "one choice won, whichever it was")
	h.mu.Lock()
	defer h.mu.Unlock()
	pickers := 0
	for _, text := range h.texts {
		if text == "Выбери категорию:" {
			pickers++
		}
	}
	require.Equal(t, 1, pickers, "the choice is made once")
}
