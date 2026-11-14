package telegram

import (
	"fmt"
	"html"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestTruncateMiddle_zwjAndCombining(t *testing.T) {
	family := "👨‍👩‍👧" // one glyph, five runes
	tests := []struct {
		name string
		in   string
		n    int
	}{
		{"zwj family", strings.Repeat(family, 6), 12},
		{"flags", strings.Repeat("🇰🇿", 10), 7},
		{"combining accent", strings.Repeat("е́", 20), 9},
		{"huge", strings.Repeat("я", 100000), 30},
		{"empty", "", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.in, tt.n)
			require.True(t, utf8.ValidString(got), "never cuts inside a rune")
			require.LessOrEqual(t, utf8.RuneCountInString(got), tt.n)
		})
	}
}

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
		{"drive not connected", false, pending, "📄 Pending"},
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
		Category: docsbot.CatAuto, Docs: []docsbot.Document{doc}, Page: 2, Pages: 3, Total: 1, DriveConnected: true,
	})
	btn := kb.InlineKeyboard[0][0]
	require.Equal(t, "d:oc:x1:l:auto:2", btn.CallbackData)
	require.Equal(t, "📥 📄 Doc", btn.Text, "no date in the row")
}

func TestRenderFound_opensCards(t *testing.T) {
	text, kb := renderFound(docsbot.Found{DriveConnected: true, Docs: []docsbot.Document{
		{ID: "x1", Title: "Паспорт", Category: docsbot.CatPassport},
	}})
	require.Equal(t, "Нашёл 1 документ:", text)
	btn := kb.InlineKeyboard[0][0]
	require.Equal(t, "📥 📄 Паспорт", btn.Text, "same row format as the list, no category")
	require.True(t, strings.HasPrefix(btn.CallbackData, "d:oc:"))
	require.True(t, strings.HasSuffix(btn.CallbackData, ":s"))

	text, kb = renderFound(docsbot.Found{Query: "привет"})
	require.Equal(t, "По запросу «привет» ничего нет. Я ищу по названиям документов — попробуй другое слово или открой /list.", text)
	require.Nil(t, kb)
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

func TestRenderCategoryPage_folderRowAboveBack(t *testing.T) {
	_, kb := renderCategoryPage(docsbot.CategoryPage{Category: docsbot.CatOther, Page: 1, Pages: 1, FolderID: "fld"})
	rows := kb.InlineKeyboard
	require.GreaterOrEqual(t, len(rows), 2)
	require.Equal(t, btnOpenCategoryFolder, rows[len(rows)-2][0].Text)
	require.Equal(t, btnBackToCats, rows[len(rows)-1][0].Text)
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
			require.Contains(t, status, html.EscapeString(truncateMiddle(title, listTitleRunes)))

			single, _ := renderSingleStatus(docsbot.StatusMessage{ID: "sm"}, doc, nil)
			require.NotContains(t, single, title)
			require.Contains(t, single, html.EscapeString(title))
		})
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

func TestListLabel(t *testing.T) {
	tests := []struct {
		name      string
		doc       docsbot.Document
		connected bool // Google Drive is linked
		want      string
	}{
		{"pdf in drive", docsbot.Document{Title: "ВНЖ", MIME: "application/pdf", Kind: docsbot.KindDocument, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK}, true, "☁️ 📄 ВНЖ"},
		{"photo in telegram", docsbot.Document{Title: "Фото", Kind: docsbot.KindPhoto}, true, "📥 🖼 Фото"},
		{"image document", docsbot.Document{Title: "Скан", MIME: "image/png", Kind: docsbot.KindDocument}, true, "📥 🖼 Скан"},
		{"drive file deleted", docsbot.Document{Title: "Договор", MIME: "application/pdf", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing}, true, "📥 📄 Договор"},
		{"drive sync pending still cloud", docsbot.Document{Title: "Х", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncPending}, true, "☁️ 📄 Х"},
		{"not connected: no storage icon", docsbot.Document{Title: "Скан", MIME: "image/png", Kind: docsbot.KindDocument}, false, "🖼 Скан"},
		{"not connected, file in drive keeps its icon", docsbot.Document{Title: "ВНЖ", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK}, false, "☁️ 📄 ВНЖ"},
		{"too large", docsbot.Document{Title: "Огромный", BackupStatus: docsbot.BackupTooLarge}, true, "📥 📄 Огромный"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, listLabel(tt.doc, tt.connected))
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

func TestRenderSettings_noExportButton(t *testing.T) {
	for _, connected := range []bool{false, true} {
		_, kb := renderSettings(docsbot.SettingsView{DriveConnected: connected, Total: 5}, "https://x", true, screenInfo{})
		for _, row := range kb.InlineKeyboard {
			for _, btn := range row {
				require.NotEqual(t, "d:exp", btn.CallbackData)
				require.NotContains(t, btn.Text, "Прислать все")
			}
		}
	}
}

// pluralCases are counts around every Russian plural boundary with the noun
// forms expected for "документ", "файл" and "изображение".
var pluralCases = []struct {
	n                    int
	document, file, imag string
}{
	{1, "документ", "файл", "изображение"},
	{2, "документа", "файла", "изображения"},
	{4, "документа", "файла", "изображения"},
	{5, "документов", "файлов", "изображений"},
	{11, "документов", "файлов", "изображений"},
	{12, "документов", "файлов", "изображений"},
	{21, "документ", "файл", "изображение"},
	{22, "документа", "файла", "изображения"},
	{25, "документов", "файлов", "изображений"},
	{101, "документ", "файл", "изображение"},
	{111, "документов", "файлов", "изображений"},
}

func TestRenderCounts_plural(t *testing.T) {
	for _, tt := range pluralCases {
		t.Run(fmt.Sprint(tt.n), func(t *testing.T) {
			want := fmt.Sprintf("%d %s", tt.n, tt.document)

			docs := make([]docsbot.Document, tt.n)
			for i := range docs {
				docs[i] = docsbot.Document{ID: docsbot.DocumentID(fmt.Sprint(i)), Title: "Док"}
			}
			found, _ := renderFound(docsbot.Found{Docs: docs})
			require.Equal(t, "Нашёл "+want+":", found)

			page, _ := renderCategoryPage(docsbot.CategoryPage{Category: docsbot.CatPassport, Total: tt.n, Page: 1, Pages: 1})
			require.Equal(t, docsbot.CatPassport.Label()+" · "+want, page)

			catalog, _ := renderCatalog(docsbot.Catalog{
				Counts:         []docsbot.CategoryCount{{Category: docsbot.CatPassport, Count: tt.n}},
				NotInDrive:     tt.n,
				DriveConnected: true,
			})
			require.Contains(t, catalog, fmt.Sprintf("📥 %s ещё не на Google Диске", want))
		})
	}
}

func TestRenderCatalog_notInDriveLine(t *testing.T) {
	counts := []docsbot.CategoryCount{{Category: docsbot.CatPassport, Count: 3}}
	tests := []struct {
		name string
		c    docsbot.Catalog
		line bool
	}{
		{"drive connected, some not uploaded", docsbot.Catalog{Counts: counts, NotInDrive: 2, DriveConnected: true}, true},
		{"drive connected, all uploaded", docsbot.Catalog{Counts: counts, DriveConnected: true}, false},
		{"drive not connected: everything is in Telegram, nothing to say", docsbot.Catalog{Counts: counts, NotInDrive: 3}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, kb := renderCatalog(tt.c)
			require.Equal(t, tt.line, strings.Contains(text, "ещё не на Google Диске"), text)
			var transfer bool
			for _, row := range kb.InlineKeyboard {
				for _, btn := range row {
					transfer = transfer || btn.CallbackData == docCB(actTransfer, "")
				}
			}
			require.Equal(t, tt.line, transfer, "the transfer button goes with the line")
		})
	}
}

func TestRenderFound_driveIcons(t *testing.T) {
	telegramOnly := docsbot.Document{ID: "t", Title: "Только тут"}
	inDrive := docsbot.Document{ID: "d", Title: "В диске", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncOK}
	tests := []struct {
		name      string
		connected bool
		want      []string
	}{
		{"drive off: no storage icons at all", false, []string{"📄 Только тут", "☁️ 📄 В диске"}},
		{"drive on: telegram-only is marked", true, []string{"📥 📄 Только тут", "☁️ 📄 В диске"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, kb := renderFound(docsbot.Found{DriveConnected: tt.connected, Docs: []docsbot.Document{telegramOnly, inDrive}})
			page := docsbot.CategoryPage{DriveConnected: tt.connected, Docs: []docsbot.Document{telegramOnly, inDrive}, Total: 2, Page: 1, Pages: 1}
			_, pageKB := renderCategoryPage(page)
			for i, want := range tt.want {
				require.Equal(t, want, kb.InlineKeyboard[i][0].Text)
				require.Equal(t, want, pageKB.InlineKeyboard[i][0].Text)
			}
		})
	}
}

func TestRenderFound_queryIsShownAsTyped(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"markup characters", "<b>&"},
		{"quotes", `«ё"'`},
		{"percent verbs", "%s %d %v"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Search answers are sent as plain text, so the query goes in verbatim.
			text, kb := renderFound(docsbot.Found{Query: tt.query})
			require.Equal(t, "По запросу «"+tt.query+"» ничего нет. Я ищу по названиям документов — попробуй другое слово или открой /list.", text)
			require.Nil(t, kb)
		})
	}
}

func TestRenderSettings_screenInfoLines(t *testing.T) {
	tests := []struct {
		name string
		v    docsbot.SettingsView
		info screenInfo
		want []string
		not  []string
	}{
		{
			"not connected names the folder", docsbot.SettingsView{},
			screenInfo{driveFolder: "Most Docs"},
			[]string{"Google Диск: не подключён", "в папку «Most Docs» на твоём Google Диске"},
			[]string{"Отправка из любого чата", "Как я обращаюсь"},
		},
		{
			"connected does not repeat the folder", docsbot.SettingsView{DriveConnected: true, Total: 3, InDrive: 2, OnlyTelegram: 1},
			screenInfo{driveFolder: "Most Docs"},
			[]string{"Google Диск: подключён ✅", "Документов: 3", "☁️ На Google Диске: 2", "📥 Только в Telegram: 1"},
			[]string{"Most Docs"},
		},
		{
			"username and url", docsbot.SettingsView{},
			screenInfo{driveFolder: "Most Docs", botUsername: "most_files_bot", privacyURL: "https://x.example/privacy"},
			[]string{"набери @most_files_bot в любом чате", "Как я обращаюсь с данными: https://x.example/privacy"},
			nil,
		},
		{
			"failed uploads are counted", docsbot.SettingsView{DriveConnected: true, Failed: 4},
			screenInfo{},
			[]string{"⚠️ Не загрузились: 4"},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _ := renderSettings(tt.v, "https://auth.example", true, tt.info)
			for _, w := range tt.want {
				require.Contains(t, text, w)
			}
			for _, n := range tt.not {
				require.NotContains(t, text, n)
			}
			require.NotContains(t, text, "%!")
		})
	}
}

func TestHelpText_fitsAMessageWithTheLongestOptionalLines(t *testing.T) {
	d := &docs{botUsername: strings.Repeat("a", 32), privacyURL: "https://" + strings.Repeat("a", 2000) + ".example/privacy"}

	require.LessOrEqual(t, utf8.RuneCountInString(d.helpText()), 4096)
}
