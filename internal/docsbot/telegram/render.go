package telegram

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// renderCatalog lists categories with counts; nil keyboard means an empty list.
func renderCatalog(c docsbot.Catalog) (string, *models.InlineKeyboardMarkup) {
	if len(c.Counts) == 0 {
		return textCatalogEmpty, nil
	}
	b := &strings.Builder{}
	b.WriteString(textCatalogTitle)
	if c.DriveConnected && c.NotInDrive > 0 {
		fmt.Fprintf(b, "\n\n"+textNotInDrive, c.NotInDrive)
	}
	counts := make(map[docsbot.Category]int, len(c.Counts))
	cats := make([]docsbot.Category, 0, len(c.Counts))
	for _, cc := range c.Counts {
		counts[cc.Category] = cc.Count
		cats = append(cats, cc.Category)
	}
	rows := categoryRows(cats, func(cat docsbot.Category) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{
			Text:         fmt.Sprintf("%s (%d)", cat.Label(), counts[cat]),
			CallbackData: docCB(actListCat, "", string(cat), "1"),
		}
	})
	if c.DriveConnected && c.NotInDrive > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnTransfer, CallbackData: docCB(actTransfer, "")}})
	}
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// renderSettings draws the /settings screen: Drive state, document counts, and
// the available actions. connectURL is the Google consent link; haveURL is false
// when it could not be built (then no connect/reconnect button is shown).
func renderSettings(v docsbot.SettingsView, connectURL string, haveURL bool) (string, *models.InlineKeyboardMarkup) {
	b := &strings.Builder{}
	if v.DriveConnected {
		b.WriteString(textSettingsOn)
	} else {
		b.WriteString(textSettingsOff)
	}
	fmt.Fprintf(b, "\n\n"+textSettingsTotal, v.Total)
	if v.DriveConnected {
		fmt.Fprintf(b, "\n"+textSettingsInDrive, v.InDrive)
		fmt.Fprintf(b, "\n"+textSettingsTelegram, v.OnlyTelegram)
	}
	if v.Failed > 0 {
		fmt.Fprintf(b, "\n"+textSettingsFailed, v.Failed)
	}

	var rows [][]models.InlineKeyboardButton
	if haveURL {
		label := btnConnectDrive
		if v.DriveConnected {
			label = btnReconnect
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, URL: connectURL}})
	}
	if v.DriveConnected {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnDisconnect, CallbackData: docCB(actDisconnect, "")}})
	}
	// Retry only makes sense while Drive is connected; a disconnected retry would
	// re-fail every document and spam reconnect prompts.
	if v.DriveConnected && v.Failed > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: fmt.Sprintf("%s (%d)", btnRetryAll, v.Failed), CallbackData: docCB(actRetryAll, ""),
		}})
	}
	rows = append(rows, inlineToggleRow(v.InlineEnabled))
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnDeleteData, CallbackData: docCB(actDeleteData, "")}})
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// renderInline turns an inline answer into Telegram results and, for the states
// that have nothing to show, the button above them that opens the bot. Results
// carry no caption: the file is sent as it is stored.
func renderInline(res docsbot.InlineResult) ([]models.InlineQueryResult, *models.InlineQueryResultsButton) {
	results := make([]models.InlineQueryResult, 0, len(res.Docs))
	for _, doc := range res.Docs {
		desc := inlineDescription(doc)
		if doc.Kind == docsbot.KindPhoto {
			results = append(results, &models.InlineQueryResultCachedPhoto{
				ID: string(doc.ID), PhotoFileID: doc.TgFileID, Title: doc.Title, Description: desc,
			})
			continue
		}
		results = append(results, &models.InlineQueryResultCachedDocument{
			ID: string(doc.ID), DocumentFileID: doc.TgFileID, Title: doc.Title, Description: desc,
		})
	}
	var text string
	switch res.State {
	case docsbot.InlineUnknown:
		text = btnInlineUnknown
	case docsbot.InlineEmpty:
		text = btnInlineEmpty
	case docsbot.InlineDisabled:
		text = btnInlineDisabled
	default:
		return results, nil
	}
	return results, &models.InlineQueryResultsButton{Text: text, StartParameter: startInline}
}

// inlineDescription is the grey line under a result: «Category · type · size».
func inlineDescription(doc docsbot.Document) string {
	parts := []string{doc.Category.Label()}
	if t := fileType(doc); t != "" {
		parts = append(parts, t)
	}
	if sz := humanSize(doc.FileSize); sz != "" {
		parts = append(parts, sz)
	}
	return strings.Join(parts, " · ")
}

// renderCategoryPage lists one page of a category, a card-opening button per document.
func renderCategoryPage(p docsbot.CategoryPage) (string, *models.InlineKeyboardMarkup) {
	text := fmt.Sprintf("%s — %d", p.Category.Label(), p.Total)
	var rows [][]models.InlineKeyboardButton
	for _, doc := range p.Docs {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         listLabel(doc),
			CallbackData: docCB(actOpenCard, doc.ID, "l", string(p.Category), strconv.Itoa(p.Page)),
		}})
	}
	if nav := paginationRow(p); nav != nil {
		rows = append(rows, nav)
	}
	if p.FolderID != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnOpenCategoryFolder, URL: folderURL(p.FolderID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnBackToCats, CallbackData: docCB(actListBack, "")}})
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// paginationRow shows ◀ page/pages ▶; arrows appear only where there is a page.
func paginationRow(p docsbot.CategoryPage) []models.InlineKeyboardButton {
	if p.Pages <= 1 {
		return nil
	}
	var row []models.InlineKeyboardButton
	if p.Page > 1 {
		row = append(row, models.InlineKeyboardButton{
			Text: "◀", CallbackData: docCB(actListCat, "", string(p.Category), strconv.Itoa(p.Page-1)),
		})
	}
	row = append(row, models.InlineKeyboardButton{
		Text: fmt.Sprintf("%d/%d", p.Page, p.Pages), CallbackData: docCB(actNoop, ""),
	})
	if p.Page < p.Pages {
		row = append(row, models.InlineKeyboardButton{
			Text: "▶", CallbackData: docCB(actListCat, "", string(p.Category), strconv.Itoa(p.Page+1)),
		})
	}
	return row
}

// renderFound lists search results; nil keyboard means nothing was found.
func renderFound(f docsbot.Found) (string, *models.InlineKeyboardMarkup) {
	if f.BlankQuery {
		return textSearchHint, nil
	}
	if len(f.Docs) == 0 {
		return textNothingFound, nil
	}
	var rows [][]models.InlineKeyboardButton
	for _, doc := range f.Docs {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         listLabel(doc),
			CallbackData: docCB(actOpenCard, doc.ID, "s"),
		}})
	}
	return fmt.Sprintf(textFoundTitle, len(f.Docs)), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// listTitleRunes is how many runes of a title one list or search button shows.
const listTitleRunes = 30

// listLabel is a document's button text in a list or search result:
// "<storage> <type> <title>", e.g. "☁️ 📄 ВНЖ". The storage icon says where the
// file lives (☁️ in Drive, 📥 only in Telegram, also when its Drive file is gone),
// the type icon what it is (🖼 image, 📄 anything else).
func listLabel(doc docsbot.Document) string {
	return storageIcon(doc) + " " + typeIcon(doc) + " " + truncateMiddle(doc.Title, listTitleRunes)
}

func storageIcon(doc docsbot.Document) string {
	if doc.InDrive() && doc.SyncStatus != docsbot.SyncMissing {
		return "☁️"
	}
	return "📥"
}

func typeIcon(doc docsbot.Document) string {
	if doc.Kind == docsbot.KindPhoto || strings.HasPrefix(doc.MIME, "image/") {
		return "🖼"
	}
	return "📄"
}

// truncateMiddle shortens s to n runes by cutting the middle, so both the start
// and the end (often a number or a year) stay visible: "Договор аренды…2026".
func truncateMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	tail := (n - 1) / 2
	head := n - 1 - tail
	return strings.TrimRight(string(r[:head]), " ") + "…" + strings.TrimLeft(string(r[len(r)-tail:]), " ")
}

// truncate shortens s to n runes, adding an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// plural picks the Russian noun form for n: one (1, 21), few (2-4, 22-24) or
// many (0, 5-20, 25-30...).
func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return many
	case n%10 == 1:
		return one
	case n%10 >= 2 && n%10 <= 4:
		return few
	default:
		return many
	}
}
