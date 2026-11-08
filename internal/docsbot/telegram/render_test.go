package telegram

import (
	"html"
	"regexp"
	"strings"
	"testing"

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
		notWant   string
	}{
		{"backed up", true, backedUp, "☁️ Backed", "📥"},
		{"not backed up", true, pending, "📥 Pending", "☁️"},
		{"drive not connected", false, backedUp, "Backed", "☁️"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _ := renderCategoryPage(docsbot.CategoryPage{
				Category: docsbot.CatOther, Docs: []docsbot.Document{tt.doc},
				Page: 1, Pages: 1, Total: 1, DriveConnected: tt.connected,
			})
			require.Contains(t, text, tt.want)
			require.NotContains(t, text, tt.notWant)
		})
	}
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
			top := cardKeyboard(doc, tt.folderID).InlineKeyboard[0]
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
	kb := cardKeyboard(doc, "")
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			require.NotEqual(t, btnOpenFolder, btn.Text)
			require.NotEqual(t, btnOpenDrive, btn.Text)
		}
	}
	require.Len(t, cardKeyboard(doc, "fld").InlineKeyboard[0], 2) // send + folder
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
