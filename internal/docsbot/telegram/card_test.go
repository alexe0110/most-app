package telegram

import (
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
			[]string{"📄 <b>a</b>\n", "🗂 Другое · PDF · 204 КБ\n", "📅 05.03.2026\n", "☁️ Google Диск · копия в Telegram"}, []string{"📥"},
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
			[]string{"📄 <b>ВНЖ</b>\n🗂 Паспорт и ID · PDF · 3 стр. · 194 КБ\n📅 10.11.2026\n☁️ Google Диск · копия в Telegram"}, nil,
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
			[]string{"📥 Файл удалён в Google Диске, копия осталась в Telegram"}, nil,
		},
		{
			"sync failed keeps its line",
			docsbot.Document{Title: "x", CreatedAt: created, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
			[]string{"⚠️ Не удалось обновить в Google Диске"}, nil,
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

func TestFolderURL(t *testing.T) {
	require.Equal(t, "https://drive.google.com/drive/folders/abc_-1", folderURL("abc_-1"))
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

func TestStorageLine(t *testing.T) {
	tests := []struct {
		st   docsbot.BackupStatus
		want string
	}{
		{docsbot.BackupPending, "⏳ Загружаю на Google Диск…"},
		{docsbot.BackupUploading, "⏳ Загружаю на Google Диск…"},
		{docsbot.BackupBackedUp, "☁️ Google Диск · копия в Telegram"},
		{docsbot.BackupFailed, "❌ Не удалось выгрузить на Google Диск"},
		{docsbot.BackupTooLarge, "📥 Слишком большой для Google Диска"},
		{docsbot.BackupNone, "📥 Только в Telegram"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, storageLine(docsbot.Document{BackupStatus: tt.st}), tt.st)
	}
}

func TestStorageLine_fileDeletedInDrive(t *testing.T) {
	doc := docsbot.Document{BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing}
	require.Equal(t, "📥 Файл удалён в Google Диске, копия осталась в Telegram", storageLine(doc))
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

func TestCardKeyboard_noBackWithoutOrigin(t *testing.T) {
	kb := cardKeyboard(docsbot.Document{ID: "1"}, "", "")
	require.Len(t, kb.InlineKeyboard, 3)
	require.Contains(t, kb.InlineKeyboard[1][0].Text, "Имя")
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
