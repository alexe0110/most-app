package telegram

import (
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
