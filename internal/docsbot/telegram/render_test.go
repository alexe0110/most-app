package telegram

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestRenderCategoryPage_driveBadge(t *testing.T) {
	backedUp := docsbot.Document{ID: "1", Title: "Backed", BackupStatus: docsbot.BackupBackedUp}
	pending := docsbot.Document{ID: "2", Title: "Pending"}

	tests := []struct {
		name      string
		connected bool
		doc       docsbot.Document
		want      string
	}{
		{"backed up", true, backedUp, "☁️ Backed"},
		{"not backed up", true, pending, "📥 Pending"},
		{"drive not connected", false, backedUp, "📄 Backed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, kb := renderCategoryPage(docsbot.CategoryPage{
				Category: docsbot.CatOther, Docs: []docsbot.Document{tt.doc},
				Page: 1, Pages: 1, Total: 1, DriveConnected: tt.connected,
			})
			require.NotContains(t, text, tt.doc.Title, "page text has no duplicated names")
			require.Contains(t, kb.InlineKeyboard[0][0].Text, tt.want)
			require.True(t, strings.HasPrefix(kb.InlineKeyboard[0][0].CallbackData, "d:oc:"))
		})
	}
}

func TestRenderCategoryPage_openCardData(t *testing.T) {
	doc := docsbot.Document{ID: "x1", Title: "Doc", CreatedAt: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)}
	_, kb := renderCategoryPage(docsbot.CategoryPage{
		Category: docsbot.CatAuto, Docs: []docsbot.Document{doc}, Page: 2, Pages: 3, Total: 1,
	})
	btn := kb.InlineKeyboard[0][0]
	require.Equal(t, "d:oc:x1:l:auto:2", btn.CallbackData)
	require.Equal(t, "📄 Doc · 05.03", btn.Text)
}

func TestRenderFound_opensCards(t *testing.T) {
	text, kb := renderFound(docsbot.Found{Docs: []docsbot.Document{
		{ID: "x1", Title: "Паспорт", Category: docsbot.CatPassport},
	}})
	require.Equal(t, "Нашёл (1):", text)
	btn := kb.InlineKeyboard[0][0]
	require.Contains(t, btn.Text, categoryLabel(docsbot.CatPassport))
	require.True(t, strings.HasPrefix(btn.CallbackData, "d:oc:"))
	require.True(t, strings.HasSuffix(btn.CallbackData, ":s"))

	text, kb = renderFound(docsbot.Found{})
	require.Equal(t, textNothingFound, text)
	require.Nil(t, kb)
}

func TestCardText(t *testing.T) {
	created := time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		doc     docsbot.Document
		want    []string
		notWant []string
	}{
		{
			"drive with size and type",
			docsbot.Document{Title: "a", MIME: "application/pdf", FileSize: 204 * 1024, CreatedAt: created,
				BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f"},
			[]string{"Добавлен: 05.03.2026 · PDF · 204 КБ", "☁️ В Google Drive"}, []string{"📥"},
		},
		{
			"telegram only, no size and type",
			docsbot.Document{Title: "a", CreatedAt: created},
			[]string{"Добавлен: 05.03.2026\n", "📥 Только в Telegram, ещё не в Drive"}, []string{" · "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cardText(tt.doc)
			for _, w := range tt.want {
				require.Contains(t, got, w)
			}
			for _, w := range tt.notWant {
				require.NotContains(t, got, w)
			}
		})
	}
}

func TestFileType(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want string
	}{
		{"pdf", docsbot.Document{MIME: "application/pdf"}, "PDF"},
		{"jpeg", docsbot.Document{MIME: "image/jpeg"}, "JPG"},
		{"jpg", docsbot.Document{MIME: "image/jpg"}, "JPG"},
		{"png", docsbot.Document{MIME: "image/png"}, "PNG"},
		{"extension", docsbot.Document{Title: "паспорт.docx"}, "DOCX"},
		{"photo", docsbot.Document{Kind: docsbot.KindPhoto, Title: "Паспорт"}, "Фото"},
		{"unknown", docsbot.Document{Kind: docsbot.KindDocument, Title: "Паспорт"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fileType(tt.doc))
		})
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, ""}, {-5, ""}, {1, "1 КБ"}, {204 * 1024, "204 КБ"}, {1363149, "1,3 МБ"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, humanSize(tt.n), "n=%d", tt.n)
	}
}

func TestCardKeyboard_backButton(t *testing.T) {
	doc := docsbot.Document{ID: "1"}
	require.Len(t, cardKeyboard(doc, "", "").InlineKeyboard, 2)
	rows := cardKeyboard(doc, "", "s").InlineKeyboard
	require.Len(t, rows, 3)
	require.Equal(t, btnBack, rows[2][0].Text)
	require.Equal(t, "d:sb", rows[2][0].CallbackData)
}

func TestBackFromOrig(t *testing.T) {
	require.Equal(t, "d:lc:auto:2", backFromOrig("l:auto:2"))
	require.Equal(t, "d:sb", backFromOrig("s"))
	require.Empty(t, backFromOrig(""))
}

func TestToastFor(t *testing.T) {
	require.Equal(t, toastSending, toastFor([]string{"d", actSend, "1"}))
	require.Equal(t, toastDone, toastFor([]string{"d", actSetCategory, "1", "other"}))
	require.Equal(t, toastDone, toastFor([]string{"d", actDeleteYes, "1"}))
	require.Empty(t, toastFor([]string{"d", actRename, "1"}))
	require.Empty(t, toastFor([]string{"d"}))
}

func TestCardKeyboard_folderButton(t *testing.T) {
	doc := docsbot.Document{ID: "1", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f1"}
	tests := []struct {
		name     string
		folderID string
		want     bool
	}{
		{"with folder", "fld", true},
		{"without folder", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			top := cardKeyboard(doc, tt.folderID, "").InlineKeyboard[0]
			var found bool
			for _, btn := range top {
				if btn.Text == btnOpenFolder {
					found = true
					require.Equal(t, "https://drive.google.com/drive/folders/fld", btn.URL)
				}
			}
			require.Equal(t, tt.want, found)
		})
	}
}

func TestRenderCategoryPage_folderButton(t *testing.T) {
	tests := []struct {
		name     string
		folderID string
		want     bool
	}{
		{"with folder", "fld", true},
		{"without folder", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, kb := renderCategoryPage(docsbot.CategoryPage{
				Category: docsbot.CatOther, Page: 1, Pages: 1, FolderID: tt.folderID,
			})
			var found bool
			for _, row := range kb.InlineKeyboard {
				for _, btn := range row {
					if btn.Text == btnOpenCategoryFolder {
						found = true
						require.Equal(t, "https://drive.google.com/drive/folders/fld", btn.URL)
					}
				}
			}
			require.Equal(t, tt.want, found)
		})
	}
}

func TestFolderURL(t *testing.T) {
	require.Equal(t, "https://drive.google.com/drive/folders/abc_-1", folderURL("abc_-1"))
}

func TestRenderCategoryPage_folderRowAboveBack(t *testing.T) {
	_, kb := renderCategoryPage(docsbot.CategoryPage{Category: docsbot.CatOther, Page: 1, Pages: 1, FolderID: "fld"})
	rows := kb.InlineKeyboard
	require.GreaterOrEqual(t, len(rows), 2)
	require.Equal(t, btnOpenCategoryFolder, rows[len(rows)-2][0].Text)
	require.Equal(t, btnBackToCats, rows[len(rows)-1][0].Text)
}

func TestCardKeyboard_folderWithoutDrive(t *testing.T) {
	doc := docsbot.Document{ID: "1"}
	kb := cardKeyboard(doc, "", "")
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			require.NotEqual(t, btnOpenFolder, btn.Text)
			require.NotEqual(t, btnOpenDrive, btn.Text)
		}
	}
	require.Len(t, cardKeyboard(doc, "fld", "").InlineKeyboard[0], 2) // send + folder
}

func statusDoc(title string, st docsbot.BackupStatus) docsbot.Document {
	return docsbot.Document{ID: "d", Title: title, Category: docsbot.CatPassport, BackupStatus: st, DriveFileID: "F1"}
}

func TestRenderStatusMessage_lines(t *testing.T) {
	tests := []struct {
		name    string
		doc     docsbot.Document
		folders map[docsbot.Category]string
		want    []string
		notWant []string
	}{
		{
			name:    "backed up with folder",
			doc:     statusDoc("a.pdf", docsbot.BackupBackedUp),
			folders: map[docsbot.Category]string{docsbot.CatPassport: "FOLD"},
			want:    []string{"✅ a.pdf · Паспорт и ID", `<a href="https://drive.google.com/file/d/F1/view">файл</a>`, `<a href="https://drive.google.com/drive/folders/FOLD">папка</a>`},
		},
		{
			name:    "backed up without folder",
			doc:     statusDoc("a.pdf", docsbot.BackupBackedUp),
			want:    []string{"✅ a.pdf", "файл</a>"},
			notWant: []string{"папка"},
		},
		{name: "pending", doc: statusDoc("a.pdf", docsbot.BackupPending), want: []string{"⏳ a.pdf · Паспорт и ID"}},
		{name: "uploading", doc: statusDoc("a.pdf", docsbot.BackupUploading), want: []string{"⏳ a.pdf"}},
		{name: "failed", doc: statusDoc("a.pdf", docsbot.BackupFailed), want: []string{"❌ a.pdf · не удалось"}},
		{name: "too large", doc: statusDoc("a.pdf", docsbot.BackupTooLarge), want: []string{"📥 a.pdf · слишком большой для Drive"}},
		{name: "escaped name", doc: statusDoc("<b>x</b>&", docsbot.BackupPending), want: []string{"&lt;b&gt;x&lt;/b&gt;&amp;"}, notWant: []string{"<b>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _ := renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, []docsbot.Document{tt.doc}, tt.folders)
			for _, w := range tt.want {
				require.Contains(t, text, w)
			}
			for _, w := range tt.notWant {
				require.NotContains(t, text, w)
			}
		})
	}
}

func TestRenderStatusMessage_counterAndRetry(t *testing.T) {
	lines := []docsbot.Document{
		statusDoc("a", docsbot.BackupBackedUp),
		statusDoc("b", docsbot.BackupPending),
		statusDoc("c", docsbot.BackupTooLarge),
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, lines, nil)
	require.True(t, strings.HasPrefix(text, "Загрузка в Drive (2 из 3)"), text)
	require.Nil(t, kb, "no retry button without failed uploads")

	lines = append(lines, statusDoc("d", docsbot.BackupFailed))
	_, kb = renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, lines, nil)
	require.NotNil(t, kb)
	require.Equal(t, "d:rsm:sm", kb.InlineKeyboard[0][0].CallbackData)
	require.Equal(t, btnRetryBackup, kb.InlineKeyboard[0][0].Text)
}

func TestStorageLine(t *testing.T) {
	tests := []struct {
		st   docsbot.BackupStatus
		want string
	}{
		{docsbot.BackupPending, "⏳ Загружаю в Drive…"},
		{docsbot.BackupUploading, "⏳ Загружаю в Drive…"},
		{docsbot.BackupBackedUp, "☁️ В Google Drive"},
		{docsbot.BackupFailed, "❌ Не удалось выгрузить в Drive"},
		{docsbot.BackupTooLarge, "📥 Слишком большой для Drive"},
		{docsbot.BackupNone, "📥 Только в Telegram, ещё не в Drive"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, storageLine(docsbot.Document{BackupStatus: tt.st}), tt.st)
	}
}

func TestRenderStatusMessage_single(t *testing.T) {
	sm := docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusSingle}

	text, kb := renderStatusMessage(sm, []docsbot.Document{statusDoc("<a>.pdf", docsbot.BackupPending)}, nil)
	require.Contains(t, text, "📄 &lt;a&gt;.pdf")
	require.Contains(t, text, "Категория: Паспорт и ID")
	require.Contains(t, text, "⏳ Загружаю в Drive…")
	require.Equal(t, cb(actSend, "d"), kb.InlineKeyboard[0][0].CallbackData)
	require.Len(t, kb.InlineKeyboard, 2, "card buttons only")

	text, kb = renderStatusMessage(sm, []docsbot.Document{statusDoc("a.pdf", docsbot.BackupBackedUp)},
		map[docsbot.Category]string{docsbot.CatPassport: "FOLD"})
	require.Contains(t, text, "☁️ В Google Drive")
	require.Equal(t, btnOpenDrive, kb.InlineKeyboard[0][1].Text)
	require.Equal(t, btnOpenFolder, kb.InlineKeyboard[0][2].Text)

	_, kb = renderStatusMessage(sm, []docsbot.Document{statusDoc("a.pdf", docsbot.BackupFailed)}, nil)
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	require.Equal(t, btnRetryBackup, last[0].Text)
	require.Equal(t, "d:rsm:sm", last[0].CallbackData)

	text, _ = renderStatusMessage(sm, []docsbot.Document{statusDoc("a", docsbot.BackupPending), statusDoc("b", docsbot.BackupPending)}, nil)
	require.True(t, strings.HasPrefix(text, "Загрузка в Drive (0 из 2)"), "more than one line falls back to a batch")
}

func TestRenderStatusMessage_batchHint(t *testing.T) {
	text, _ := renderStatusMessage(docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusBatch},
		[]docsbot.Document{statusDoc("a", docsbot.BackupPending)}, nil)
	require.Contains(t, text, "✏️ Изменить имя и категорию: /list")
}

func TestRenderStatusMessage_transfer(t *testing.T) {
	sm := docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusTransfer}

	text, _ := renderStatusMessage(sm, []docsbot.Document{
		statusDoc("a", docsbot.BackupBackedUp), statusDoc("b", docsbot.BackupPending)}, nil)
	require.True(t, strings.HasPrefix(text, "Перенос в Drive (1 из 2)"), text)
	require.NotContains(t, text, "Готово")

	text, kb := renderStatusMessage(sm, []docsbot.Document{
		statusDoc("a", docsbot.BackupBackedUp), statusDoc("b", docsbot.BackupTooLarge)}, nil)
	require.Contains(t, text, "Готово: 1 в Drive")
	require.Nil(t, kb)

	_, kb = renderStatusMessage(sm, []docsbot.Document{statusDoc("a", docsbot.BackupFailed)}, nil)
	require.NotNil(t, kb)
}

func TestRenderStatusMessage_overflow(t *testing.T) {
	var lines []docsbot.Document
	for i := 0; i < 25; i++ {
		lines = append(lines, statusDoc(strings.Repeat("я", 100), docsbot.BackupBackedUp))
	}
	text, _ := renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, lines, map[docsbot.Category]string{docsbot.CatPassport: "FOLD"})
	require.Contains(t, text, "…и ещё 5")
	require.Equal(t, 20, strings.Count(text, "✅"))
	require.Less(t, len([]rune(text)), 4096)
}

// Telegram counts the characters of the parsed text (tags stripped) against 4096.
// Use realistic Drive ids, the longest allowed name and the longest category label.
func TestRenderStatusMessage_realisticOverflowAndInjection(t *testing.T) {
	id := strings.Repeat("a", 33)
	var lines []docsbot.Document
	for i := 0; i < 60; i++ {
		d := statusDoc(strings.Repeat("<&\"", 100), docsbot.BackupBackedUp)
		d.DriveFileID = id
		lines = append(lines, d)
	}
	text, _ := renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, lines, map[docsbot.Category]string{docsbot.CatPassport: id})

	require.Equal(t, 20, strings.Count(text, "✅"))
	require.Contains(t, text, "…и ещё 40")
	require.NotContains(t, text, "<&", "names must be escaped")
	require.NotContains(t, text, `""`, "a quote in a name must not leak outside escaping")
	visible := html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(text, ""))
	require.Less(t, len([]rune(visible)), 4096, "visible text fits Telegram's limit")
}

func TestIncomingFile(t *testing.T) {
	tests := []struct {
		name string
		msg  *models.Message
		want docsbot.IncomingFile
	}{
		{
			name: "photo without caption has no title",
			msg: &models.Message{Photo: []models.PhotoSize{
				{FileID: "small", FileUniqueID: "us", FileSize: 10},
				{FileID: "big", FileUniqueID: "ub", FileSize: 2048},
			}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindPhoto, FileID: "big", FileUniqueID: "ub", FileSize: 2048},
		},
		{
			name: "photo caption becomes title",
			msg:  &models.Message{Caption: "Чек", Photo: []models.PhotoSize{{FileID: "p", FileUniqueID: "up", FileSize: 5}}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindPhoto, FileID: "p", FileUniqueID: "up", Title: "Чек", FileSize: 5},
		},
		{
			name: "document without name or caption has no title",
			msg:  &models.Message{Document: &models.Document{FileID: "d", FileUniqueID: "ud", FileSize: 7, MimeType: "application/pdf"}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud", FileSize: 7, MIME: "application/pdf"},
		},
		{
			name: "document file name is kept",
			msg: &models.Message{Caption: "подпись", Document: &models.Document{
				FileID: "d", FileUniqueID: "ud", FileName: "scan.pdf", FileSize: 99, MimeType: "application/pdf",
			}},
			want: docsbot.IncomingFile{
				UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud",
				Title: "scan.pdf", FileSize: 99, MIME: "application/pdf",
			},
		},
		{
			name: "document caption used when no file name",
			msg:  &models.Message{Caption: "подпись", Document: &models.Document{FileID: "d", FileUniqueID: "ud"}},
			want: docsbot.IncomingFile{UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud", Title: "подпись"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := incomingFile(tt.msg, "u")
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestHumanSize_boundaries(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{1024, "1 КБ"}, {1025, "2 КБ"}, {1<<20 - 1024, "1023 КБ"}, {1 << 20, "1,0 МБ"}, {5 << 20, "5,0 МБ"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, humanSize(tt.n), "n=%d", tt.n)
	}
	// Just below 1 MiB must not render as the impossible "1024 КБ".
	require.NotEqual(t, "1024 КБ", humanSize(1<<20-1))
}

// A default title "Категория — 08.11.2026" has a dotted date; it must not be read as
// a file extension.
func TestFileType_defaultAndDottedTitles(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want string
	}{
		{"default title on a photo", docsbot.Document{Kind: docsbot.KindPhoto, Title: "Медицина — 08.11.2026"}, "Фото"},
		{"default title on a document", docsbot.Document{Kind: docsbot.KindDocument, Title: "Медицина — 08.11.2026"}, ""},
		{"sentence with a dot", docsbot.Document{Kind: docsbot.KindDocument, Title: "Справка. Итоги года"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, fileType(tt.doc))
		})
	}
}

func TestRender_longTitlesAreTruncated(t *testing.T) {
	long := strings.Repeat("Я", 200)
	_, kb := renderCategoryPage(docsbot.CategoryPage{
		Category: docsbot.CatOther, Docs: []docsbot.Document{{ID: "1", Title: long}}, Page: 1, Pages: 1, Total: 1,
	})
	btn := kb.InlineKeyboard[0][0].Text
	require.Contains(t, btn, "…")
	require.Less(t, len([]rune(btn)), 60)

	_, kb = renderFound(docsbot.Found{Docs: []docsbot.Document{{ID: "1", Title: long, Category: docsbot.CatOther}}})
	btn = kb.InlineKeyboard[0][0].Text
	require.Contains(t, btn, "…")
	require.Less(t, len([]rune(btn)), 70)
}

func TestCardKeyboard_noBackWithoutOrigin(t *testing.T) {
	kb := cardKeyboard(docsbot.Document{ID: "1"}, "", "")
	require.Len(t, kb.InlineKeyboard, 2)
	require.Contains(t, kb.InlineKeyboard[1][0].Text, "Имя")
}
