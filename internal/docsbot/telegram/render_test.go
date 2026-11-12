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
		{"backed up", true, backedUp, "☁️ 📄 Backed"},
		{"not backed up", true, pending, "📥 📄 Pending"},
		{"file deleted in drive", true, docsbot.Document{ID: "3", Title: "Gone", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing}, "📥 📄 Gone"},
		{"drive not connected", false, pending, "📥 📄 Pending"},
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
	require.Equal(t, "📥 📄 Doc", btn.Text, "no date in the row")
}

func TestRenderFound_opensCards(t *testing.T) {
	text, kb := renderFound(docsbot.Found{Docs: []docsbot.Document{
		{ID: "x1", Title: "Паспорт", Category: docsbot.CatPassport},
	}})
	require.Equal(t, "Нашёл (1):", text)
	btn := kb.InlineKeyboard[0][0]
	require.Equal(t, "📥 📄 Паспорт", btn.Text, "same row format as the list, no category")
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
			[]string{"📄 <b>a</b>\n", "🗂 Другое · PDF · 204 КБ\n", "📅 05.03.2026\n", "☁️ Google Drive · копия в Telegram"}, []string{"📥"},
		},
		{
			"telegram only, no size and type",
			docsbot.Document{Title: "a", CreatedAt: created},
			[]string{"🗂 Другое\n", "📅 05.03.2026\n", "📥 Только в Telegram"}, []string{" · ", "стр."},
		},
		{
			"pdf with pages",
			docsbot.Document{Title: "ВНЖ", Category: docsbot.CatPassport, MIME: "application/pdf", FileSize: 194 * 1024, PageCount: 3,
				CreatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK},
			[]string{"📄 <b>ВНЖ</b>\n🗂 Паспорт и ID · PDF · 3 стр. · 194 КБ\n📅 10.11.2026\n☁️ Google Drive · копия в Telegram"}, nil,
		},
		{
			"photo has the image icon",
			docsbot.Document{Title: "Фото", Kind: docsbot.KindPhoto, FileSize: 2 * 1024 * 1024, CreatedAt: created},
			[]string{"🖼 <b>Фото</b>\n", "🗂 Другое · Фото · 2,0 МБ\n"}, []string{"стр."},
		},
		{
			"image with pixel size",
			docsbot.Document{Title: "Скан", MIME: "image/jpeg", Kind: docsbot.KindDocument, Width: 1280, Height: 960, FileSize: 194 * 1024, CreatedAt: created},
			[]string{"🗂 Другое · JPG · 1280×960 · 194 КБ\n"}, []string{"стр."},
		},
		{
			"half-known size is left out",
			docsbot.Document{Title: "Скан", MIME: "image/jpeg", Width: 1280, CreatedAt: created},
			[]string{"🗂 Другое · JPG\n"}, []string{"1280"},
		},
		{
			"unknown pages are left out",
			docsbot.Document{Title: "Договор", MIME: "application/pdf", CreatedAt: created},
			[]string{"🗂 Другое · PDF\n"}, []string{"стр."},
		},
		{
			"retired category reads Другое",
			docsbot.Document{Title: "x", Category: "contracts", CreatedAt: created},
			[]string{"🗂 Другое\n"}, nil,
		},
		{
			"file deleted in drive",
			docsbot.Document{Title: "x", CreatedAt: created, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing},
			[]string{"📥 Файл удалён в Drive, копия осталась в Telegram"}, nil,
		},
		{
			"sync failed keeps its line",
			docsbot.Document{Title: "x", CreatedAt: created, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			[]string{"⚠️ Не удалось обновить в Drive"}, nil,
		},
		{
			"title is escaped",
			docsbot.Document{Title: "<b>x</b> & <script>", CreatedAt: created},
			[]string{"📄 <b>&lt;b&gt;x&lt;/b&gt; &amp; &lt;script&gt;</b>\n"}, []string{"<script>", "<b>x</b>"},
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
		{"docx by mime", docsbot.Document{MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}, "DOCX"},
		{"xlsx by mime", docsbot.Document{MIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}, "XLSX"},
		{"webp", docsbot.Document{MIME: "image/webp"}, "WEBP"},
		{"heic", docsbot.Document{MIME: "image/heic"}, "HEIC"},
		{"mime parameters and case", docsbot.Document{MIME: "Application/PDF; charset=binary"}, "PDF"},
		{"the title is not consulted", docsbot.Document{Title: "паспорт.docx", Kind: docsbot.KindDocument}, ""},
		{"unknown mime on a document", docsbot.Document{MIME: "application/x-weird", Kind: docsbot.KindDocument}, ""},
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
	require.Len(t, cardKeyboard(doc, "", "").InlineKeyboard, 3)
	rows := cardKeyboard(doc, "", "s").InlineKeyboard
	require.Len(t, rows, 4)
	require.Equal(t, btnBack, rows[3][0].Text)
	require.Equal(t, "d:sb", rows[3][0].CallbackData)
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
			var found bool
			for _, row := range cardKeyboard(doc, tt.folderID, "").InlineKeyboard {
				for _, btn := range row {
					if btn.Text == btnOpenFolder {
						found = true
						require.Equal(t, "https://drive.google.com/drive/folders/fld", btn.URL)
					}
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
	require.Equal(t, btnOpenFolder, cardKeyboard(doc, "fld", "").InlineKeyboard[1][0].Text) // its own row
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
		{docsbot.BackupBackedUp, "☁️ Google Drive · копия в Telegram"},
		{docsbot.BackupFailed, "❌ Не удалось выгрузить в Drive"},
		{docsbot.BackupTooLarge, "📥 Слишком большой для Drive"},
		{docsbot.BackupNone, "📥 Только в Telegram"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, storageLine(docsbot.Document{BackupStatus: tt.st}), tt.st)
	}
}

func TestStorageLine_fileDeletedInDrive(t *testing.T) {
	doc := docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing}
	require.Equal(t, "📥 Файл удалён в Drive, копия осталась в Telegram", storageLine(doc))
}

func TestRenderStatusMessage_single(t *testing.T) {
	sm := docsbot.StatusMessage{ID: "sm", Kind: docsbot.StatusSingle}

	text, kb := renderStatusMessage(sm, []docsbot.Document{statusDoc("<a>.pdf", docsbot.BackupPending)}, nil)
	require.Contains(t, text, "📄 <b>&lt;a&gt;.pdf</b>")
	require.Contains(t, text, "🗂 Паспорт и ID")
	require.Contains(t, text, "⏳ Загружаю в Drive…")
	require.Equal(t, cb(actSend, "d"), kb.InlineKeyboard[0][0].CallbackData)
	require.Len(t, kb.InlineKeyboard, 3, "card buttons only")

	text, kb = renderStatusMessage(sm, []docsbot.Document{statusDoc("a.pdf", docsbot.BackupBackedUp)},
		map[docsbot.Category]string{docsbot.CatPassport: "FOLD"})
	require.Contains(t, text, "☁️ Google Drive · копия в Telegram")
	require.Equal(t, btnCardOpen, kb.InlineKeyboard[0][1].Text)
	require.Equal(t, btnOpenFolder, kb.InlineKeyboard[1][0].Text)

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
			name: "document file name loses its extension",
			msg: &models.Message{Caption: "подпись", Document: &models.Document{
				FileID: "d", FileUniqueID: "ud", FileName: "scan.pdf", FileSize: 99, MimeType: "application/pdf",
			}},
			want: docsbot.IncomingFile{
				UserID: "u", Kind: docsbot.KindDocument, FileID: "d", FileUniqueID: "ud",
				Title: "scan", FileSize: 99, MIME: "application/pdf",
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
	require.Len(t, kb.InlineKeyboard, 3)
	require.Contains(t, kb.InlineKeyboard[1][0].Text, "Имя")
}

// Cards and the status message are HTML and escape the title; lists and search are
// plain button text. A hostile title must be inert in every view.
func TestRender_hostileTitle(t *testing.T) {
	for _, title := range []string{"<b>x</b>", "a & b", "<script>alert(1)</script>"} {
		t.Run(title, func(t *testing.T) {
			doc := statusDoc(title, docsbot.BackupBackedUp)
			doc.ID = "d1"
			doc.CreatedAt = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)

			require.Contains(t, cardText(doc), "<b>"+html.EscapeString(title)+"</b>\n", "card is HTML: the title is escaped")
			require.NotContains(t, cardText(doc), "<b>"+title+"</b>")

			listText, listKB := renderCategoryPage(docsbot.CategoryPage{Category: docsbot.CatPassport, Docs: []docsbot.Document{doc}, Page: 1, Pages: 1, Total: 1})
			require.NotContains(t, listText, title)
			require.Contains(t, listKB.InlineKeyboard[0][0].Text, truncate(title, 32), "buttons are plain text")

			_, foundKB := renderFound(docsbot.Found{Docs: []docsbot.Document{doc}})
			require.Contains(t, foundKB.InlineKeyboard[0][0].Text, truncate(title, 32))

			status, _ := renderStatusMessage(docsbot.StatusMessage{ID: "sm"}, []docsbot.Document{doc, doc}, nil)
			require.NotContains(t, status, title, "status message is HTML: the raw title must not appear")
			require.Contains(t, status, html.EscapeString(truncate(title, statusNameLimit)))

			single, _ := renderSingleStatus(docsbot.StatusMessage{ID: "sm"}, doc, nil)
			require.NotContains(t, single, title)
			require.Contains(t, single, html.EscapeString(title))
		})
	}
}

func TestCardKeyboard_backedUpWithoutFolder(t *testing.T) {
	doc := docsbot.Document{ID: "d", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "file-1"}
	var urls []string
	for _, btn := range cardKeyboard(doc, "", "").InlineKeyboard[0] {
		urls = append(urls, btn.URL)
	}
	require.Contains(t, urls, driveURL("file-1"))
	require.NotContains(t, urls, folderURL(""))
	for _, u := range urls {
		require.NotContains(t, u, "/folders/")
	}
}

func TestPlural(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "файл"}, {2, "файла"}, {5, "файлов"}, {11, "файлов"},
		{21, "файл"}, {22, "файла"}, {25, "файлов"}, {111, "файлов"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, plural(tt.n, "файл", "файла", "файлов"), "n=%d", tt.n)
	}
}

func TestCardKeyboard_atMostTwoButtonsPerRow(t *testing.T) {
	tests := []struct {
		name     string
		doc      docsbot.Document
		folderID string
		orig     string
	}{
		{"plain", docsbot.Document{ID: "1"}, "", ""},
		{"in drive with folder", docsbot.Document{ID: "1", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f"}, "fld", ""},
		{"convertible with back", docsbot.Document{ID: "1", Kind: docsbot.KindPhoto}, "", "s"},
		{"everything", docsbot.Document{ID: "1", Kind: docsbot.KindPhoto, BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f"}, "fld", "l:auto:1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, row := range cardKeyboard(tt.doc, tt.folderID, tt.orig).InlineKeyboard {
				require.LessOrEqual(t, len(row), 2)
			}
		})
	}
}

func TestCardKeyboard_revokedSyncOffersReconnect(t *testing.T) {
	doc := docsbot.Document{
		ID: "1", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f",
		SyncStatus: docsbot.SyncFailed, SyncReason: docsbot.SyncReasonRevoked,
	}
	buttons := func(kb *models.InlineKeyboardMarkup) (texts []string, urls []string) {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				texts = append(texts, b.Text)
				urls = append(urls, b.URL)
			}
		}
		return texts, urls
	}

	texts, urls := buttons(cardKeyboardWith(doc, "", "", "https://consent"))
	require.Contains(t, texts, btnReconnect)
	require.Contains(t, urls, "https://consent")
	require.NotContains(t, texts, btnRetryBackup)

	texts, _ = buttons(cardKeyboardWith(doc, "", "", ""))
	require.NotContains(t, texts, btnReconnect, "no link, no button")
	require.NotContains(t, texts, btnRetryBackup, "retrying cannot help")

	doc.SyncReason = ""
	texts, _ = buttons(cardKeyboardWith(doc, "", "", "https://consent"))
	require.Contains(t, texts, btnRetryBackup, "an ordinary failure is retried")
	require.NotContains(t, texts, btnReconnect)
}

func TestListLabel(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want string
	}{
		{"pdf in drive", docsbot.Document{Title: "ВНЖ", MIME: "application/pdf", Kind: docsbot.KindDocument, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK}, "☁️ 📄 ВНЖ"},
		{"photo in telegram", docsbot.Document{Title: "Фото", Kind: docsbot.KindPhoto}, "📥 🖼 Фото"},
		{"image document", docsbot.Document{Title: "Скан", MIME: "image/png", Kind: docsbot.KindDocument}, "📥 🖼 Скан"},
		{"drive file deleted", docsbot.Document{Title: "Договор", MIME: "application/pdf", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing}, "📥 📄 Договор"},
		{"drive sync pending still cloud", docsbot.Document{Title: "Х", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncPending}, "☁️ 📄 Х"},
		{"too large", docsbot.Document{Title: "Огромный", BackupStatus: docsbot.BackupTooLarge}, "📥 📄 Огромный"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, listLabel(tt.doc))
		})
	}
}

func TestRenderCategoryPage_sameRowAsSearch(t *testing.T) {
	doc := docsbot.Document{ID: "1", Title: "Договор аренды квартиры на Абая 2026", Category: docsbot.CatRealty}
	_, list := renderCategoryPage(docsbot.CategoryPage{Category: docsbot.CatRealty, Docs: []docsbot.Document{doc}, Page: 1, Pages: 1, Total: 1})
	_, found := renderFound(docsbot.Found{Docs: []docsbot.Document{doc}})
	require.Equal(t, list.InlineKeyboard[0][0].Text, found.InlineKeyboard[0][0].Text)
	require.LessOrEqual(t, len([]rune(list.InlineKeyboard[0][0].Text)), 36)
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short stays", "ВНЖ", 30, "ВНЖ"},
		{"exactly n stays", "абвгд", 5, "абвгд"},
		{"cyrillic keeps both ends", "Договор аренды квартиры 2026", 15, "Договор…ры 2026"},
		{"latin", "abcdefghijklmnopqrstuvwxyz", 10, "abcde…wxyz"},
		{"emoji is not split", "😀😀😀😀😀😀😀😀", 5, "😀😀…😀😀"},
		{"tiny limit falls back", "abcdef", 2, "a…"},
		{"end stays visible", "Справка о доходах за прошлый год № 12345", 20, "Справка о…д № 12345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.in, tt.n)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len([]rune(got)), tt.n)
		})
	}
}

func TestCategoryKeyboard_twoPerRowOtherAlone(t *testing.T) {
	kb := categoryKeyboard(actCategory, "d1")
	rows := kb.InlineKeyboard
	require.Len(t, rows, 6)
	for _, row := range rows[:5] {
		require.Len(t, row, 2)
	}
	require.Len(t, rows[5], 1)
	require.Equal(t, "Другое", rows[5][0].Text)
	require.Equal(t, "d:cat:d1:other", rows[5][0].CallbackData)
	require.Equal(t, "Паспорт и ID", rows[0][0].Text)
	require.Equal(t, "Справки", rows[0][1].Text)
}

func TestRenderCatalog_layout(t *testing.T) {
	_, kb := renderCatalog(docsbot.Catalog{Counts: []docsbot.CategoryCount{
		{Category: docsbot.CatPassport, Count: 2},
		{Category: docsbot.CatAuto, Count: 1},
		{Category: docsbot.CatWork, Count: 3},
		{Category: docsbot.CatOther, Count: 4},
	}})
	require.Len(t, kb.InlineKeyboard, 3)
	require.Equal(t, "Паспорт и ID (2)", kb.InlineKeyboard[0][0].Text)
	require.Equal(t, "Авто (1)", kb.InlineKeyboard[0][1].Text)
	require.Equal(t, "Работа (3)", kb.InlineKeyboard[1][0].Text)
	require.Len(t, kb.InlineKeyboard[2], 1)
	require.Equal(t, "Другое (4)", kb.InlineKeyboard[2][0].Text)
	require.Equal(t, "d:lc:other:1", kb.InlineKeyboard[2][0].CallbackData)
}

func TestExportBatches(t *testing.T) {
	photo := func(n int) []docsbot.Document { return repeatDocs(docsbot.KindPhoto, n) }
	file := func(n int) []docsbot.Document { return repeatDocs(docsbot.KindDocument, n) }
	cat := func(parts ...[]docsbot.Document) []docsbot.Document {
		var out []docsbot.Document
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name string
		in   []docsbot.Document
		want []int // batch sizes
	}{
		{"nothing", nil, nil},
		{"one", photo(1), []int{1}},
		{"ten fit one album", photo(10), []int{10}},
		{"eleven split", photo(11), []int{10, 1}},
		{"twenty five", file(25), []int{10, 10, 5}},
		{"kinds are never mixed", cat(photo(3), file(2), photo(1)), []int{3, 2, 1}},
		{"alternating", cat(photo(1), file(1), photo(1), file(1)), []int{1, 1, 1, 1}},
		{"run continues after a switch", cat(file(1), photo(12)), []int{1, 10, 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []int
			total := 0
			for _, batch := range exportBatches(tt.in) {
				got = append(got, len(batch))
				total += len(batch)
				for _, d := range batch {
					require.Equal(t, batch[0].Kind == docsbot.KindPhoto, d.Kind == docsbot.KindPhoto)
				}
			}
			require.Equal(t, tt.want, got)
			require.Equal(t, len(tt.in), total, "nothing lost")
		})
	}
}

func repeatDocs(kind docsbot.Kind, n int) []docsbot.Document {
	out := make([]docsbot.Document, n)
	for i := range out {
		out[i] = docsbot.Document{Kind: kind}
	}
	return out
}

func TestRenderSettings_noExportButton(t *testing.T) {
	for _, connected := range []bool{false, true} {
		_, kb := renderSettings(docsbot.SettingsView{DriveConnected: connected, Total: 5}, "https://x", true)
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				require.NotEqual(t, "d:exp", btn.CallbackData)
				require.NotContains(t, btn.Text, "Прислать все")
			}
		}
	}
}

func TestCardKeyboard_layout(t *testing.T) {
	drive := docsbot.Document{ID: "d1", BackupStatus: docsbot.BackupBackedUp, DriveFileID: "f1", SyncStatus: docsbot.SyncOK, MIME: "application/pdf", Kind: docsbot.KindDocument}
	with := func(mod func(*docsbot.Document)) docsbot.Document { d := drive; mod(&d); return d }
	tests := []struct {
		name   string
		doc    docsbot.Document
		folder string
		orig   string
		want   [][]string // button labels per row
	}{
		{"telegram only, not convertible",
			docsbot.Document{ID: "d1", MIME: "text/plain"}, "", "",
			[][]string{{btnCardSend}, {btnRename, btnChangeCat}, {btnDelete}}},
		{"in drive, convertible, with folder and back",
			drive, "fld", "s",
			[][]string{{btnCardSend, btnCardOpen}, {btnOpenFolder}, {btnRename, btnChangeCat}, {btnConvertCard, btnDelete}, {btnBack}}},
		{"sync failed",
			with(func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncFailed }), "", "",
			[][]string{{btnCardSend, btnCardOpen}, {btnRetryBackup}, {btnRename, btnChangeCat}, {btnConvertCard, btnDelete}}},
		{"file deleted in drive",
			with(func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncMissing }), "", "",
			[][]string{{btnCardSend}, {btnReupload}, {btnRename, btnChangeCat}, {btnConvertCard, btnDelete}}},
		{"revoked without link has no retry",
			with(func(d *docsbot.Document) { d.SyncStatus = docsbot.SyncFailed; d.SyncReason = docsbot.SyncReasonRevoked }), "", "l:auto:2",
			[][]string{{btnCardSend, btnCardOpen}, {btnRename, btnChangeCat}, {btnConvertCard, btnDelete}, {btnBack}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got [][]string
			for _, row := range cardKeyboard(tt.doc, tt.folder, tt.orig).InlineKeyboard {
				require.LessOrEqual(t, len(row), 2, "at most two buttons per row")
				var labels []string
				for _, btn := range row {
					if len(row) == 2 {
						require.LessOrEqual(t, len([]rune(btn.Text)), 16, "short label in a shared row: %q", btn.Text)
					}
					labels = append(labels, btn.Text)
				}
				got = append(got, labels)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestIncomingFile_photoPixelSize(t *testing.T) {
	m := &models.Message{Photo: []models.PhotoSize{
		{FileID: "s", FileUniqueID: "us", Width: 90, Height: 60, FileSize: 1},
		{FileID: "l", FileUniqueID: "ul", Width: 1280, Height: 960, FileSize: 2},
	}}
	in, ok := incomingFile(m, "u")
	require.True(t, ok)
	require.Equal(t, "l", in.FileID)
	require.Equal(t, 1280, in.Width)
	require.Equal(t, 960, in.Height)

	// The Bot API gives a document no pixel size, and the bot does not download to measure.
	in, ok = incomingFile(&models.Message{Document: &models.Document{FileID: "d", FileUniqueID: "ud", MimeType: "image/png"}}, "u")
	require.True(t, ok)
	require.Zero(t, in.Width)
	require.Zero(t, in.Height)
}
