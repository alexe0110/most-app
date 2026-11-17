package telegram

import (
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

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

// builtButtons is the callback data of every inline keyboard the bot renders,
// with the worst-case values the limits care about.
func builtButtons() []string {
	slug := docsbot.Category(longestSlug())
	token := tg.NewToken()
	statusID := docsbot.StatusMessageID(uuidDoc)

	var kbs []*models.InlineKeyboardMarkup
	add := func(kb *models.InlineKeyboardMarkup) { kbs = append(kbs, kb) }
	addRender := func(_ string, kb *models.InlineKeyboardMarkup) { add(kb) }

	for _, action := range []string{actCategory, actCategoryGroup, actSetCategory} {
		add(categoryKeyboard(action, uuidDoc))
	}
	for _, doc := range []docsbot.Document{
		{ID: uuidDoc, Kind: docsbot.KindPhoto, Category: slug},
		{ID: uuidDoc, Kind: docsbot.KindDocument, MIME: "application/pdf", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed},
		{ID: uuidDoc, Kind: docsbot.KindDocument, MIME: "image/webp", BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncMissing},
		{ID: uuidDoc, Kind: docsbot.KindDocument, BackupStatus: docsbot.BackupBackedUp, SyncStatus: docsbot.SyncFailed, SyncReason: docsbot.SyncReasonRevoked},
	} {
		add(pickerKeyboard(doc))
		for _, orig := range []string{"", "s", "l:" + string(slug) + ":999"} {
			add(cardKeyboard(doc, "folder", orig))
			add(cardKeyboardWith(doc, "folder", orig, "https://example.com/connect"))
		}
		add(openDriveKeyboard(doc))
	}
	add(albumChoiceKeyboard(token, uuidDoc))
	add(renameCancelKeyboard(uuidDoc))
	add(transferOfferKeyboard())
	add(retryKeyboard(uuidDoc))
	add(deleteDataKeyboard())
	add(feedbackCancelKeyboard())
	add((&docs{}).problemKeyboard("choose_album_category")) // the longest scenario a docs error names
	add(myDocsKeyboard())
	add(retryBatchKeyboard(statusID))
	add(statusKeyboard(statusID, statusTally{total: 3, done: 3, failed: 1}))
	add(&models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{inlineToggleRow(true), inlineToggleRow(false)}})

	addRender(renderCatalog(docsbot.Catalog{
		DriveConnected: true, NotInDrive: 1,
		Counts: []docsbot.CategoryCount{{Category: slug, Count: 1}},
	}))
	addRender(renderSettings(docsbot.SettingsView{DriveConnected: true, Failed: 2, InlineEnabled: true}, "https://example.com/connect", true, screenInfo{}))
	addRender(renderCategoryPage(docsbot.CategoryPage{
		Category: slug, Page: 500, Pages: 999, Total: 1, Docs: []docsbot.Document{{ID: uuidDoc, Title: "t"}},
	}))
	addRender(renderFound(docsbot.Found{Query: "q", Docs: []docsbot.Document{{ID: uuidDoc, Title: "t"}}}))

	var data []string
	for _, kb := range kbs {
		for _, row := range kb.InlineKeyboard {
			for _, b := range row {
				if b.CallbackData != "" { // link buttons carry a URL instead
					data = append(data, b.CallbackData)
				}
			}
		}
	}
	return data
}

func TestBuiltButtons_everyOneParsesFitsAndIsKnown(t *testing.T) {
	known := map[string]bool{}
	for _, tt := range callbackCases() {
		known[tt.want.action] = true
	}

	data := builtButtons()
	require.NotEmpty(t, data)
	for _, d := range data {
		require.LessOrEqual(t, len(d), 64, d)
		if strings.HasPrefix(d, "conv:") {
			continue // the converter package pins its own data
		}
		cd, ok := parseCallback(d)
		require.True(t, ok, d)
		require.True(t, known[cd.action], "%s: an action with no round-trip case", d)
		require.Equal(t, d, cd.String(), "String inverts parse")
	}
}

func TestPickerKeyboard_infoButtonJoinsTheLastRow(t *testing.T) {
	base := len(categoryKeyboard(actCategory, uuidDoc).InlineKeyboard)
	tests := []struct {
		name string
		doc  docsbot.Document
		want int // buttons in the last row, 0 when there is no extra row
	}{
		{"jpeg: convert and info", docsbot.Document{ID: uuidDoc, Kind: docsbot.KindDocument, MIME: "image/jpeg"}, 2},
		{"heic: info alone", docsbot.Document{ID: uuidDoc, Kind: docsbot.KindDocument, MIME: "image/heic"}, 1},
		{"video: neither", docsbot.Document{ID: uuidDoc, Kind: docsbot.KindDocument, MIME: "video/mp4"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := pickerKeyboard(tt.doc).InlineKeyboard

			if tt.want == 0 {
				require.Len(t, rows, base)
				return
			}
			require.Len(t, rows, base+1)
			last := rows[len(rows)-1]
			require.Len(t, last, tt.want)
			require.Equal(t, converter.InfoButton(uuidDoc).CallbackData, last[len(last)-1].CallbackData)
		})
	}
}
