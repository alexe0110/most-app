package telegram

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// cbPrefix tags every inline button the docs feature owns, keeping it clear of
// the /convert buttons ("conv:").
const cbPrefix = "d:"

// Callback actions. Data is "d:<action>" or "d:<action>:<docID>[:<arg>]".
const (
	actCategory    = "cat" // pick a category for a new document
	actSetCategory = "sc"  // change an existing document's category
	actChangeCat   = "chc" // show the category picker for a card
	actSend        = "snd" // resend the original file
	actRename      = "rn"  // ask for a new title (force reply)
	actDeleteAsk   = "del" // ask to confirm deletion
	actDeleteYes   = "dy"  // confirm deletion
	actDeleteNo    = "dn"  // cancel deletion
	actListCat     = "lc"  // open a category page: d:lc:<slug>:<page>
	actListBack    = "lb"  // back to the category list
	actTransfer    = "tr"  // transfer saved documents to Drive (#11)
	actTransferYes = "try" // confirm the transfer offer
	actTransferNo  = "trn" // decline the transfer offer
	actRetry       = "rt"  // retry a failed backup: d:rt:<docID>
	actDisconnect  = "dc"  // disconnect Google Drive
	actRetryAll    = "rta" // retry every failed backup
	actExport      = "exp" // resend all documents from Telegram
	actDeleteData  = "dd"  // start the delete-all-data confirmation
	actNoop        = "noop"
)

// categoryLabels are the Russian names shown to the user (product.md). The
// stored value is the slug.
var categoryLabels = map[docsbot.Category]string{
	docsbot.CatPassport:  "Паспорт и ID",
	docsbot.CatContracts: "Договоры",
	docsbot.CatReceipts:  "Чеки и гарантии",
	docsbot.CatAuto:      "Авто",
	docsbot.CatMedical:   "Медицина",
	docsbot.CatEduWork:   "Учёба и работа",
	docsbot.CatOther:     "Другое",
}

func categoryLabel(c docsbot.Category) string {
	if label, ok := categoryLabels[c]; ok {
		return label
	}
	return string(c)
}

// CategoryLabel is the Russian display name for a category.
func CategoryLabel(c docsbot.Category) string { return categoryLabel(c) }

// cb builds "d:<action>:<docID>".
func cb(action string, id docsbot.DocumentID) string {
	return fmt.Sprintf("%s%s:%s", cbPrefix, action, id)
}

// categoryKeyboard offers every category, two per row. action is actCategory for
// a new document or actSetCategory when changing one.
func categoryKeyboard(action string, id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	var line []models.InlineKeyboardButton
	for _, c := range docsbot.AllCategories {
		line = append(line, models.InlineKeyboardButton{
			Text:         categoryLabel(c),
			CallbackData: fmt.Sprintf("%s%s:%s:%s", cbPrefix, action, id, c),
		})
		if len(line) == 2 {
			rows = append(rows, line)
			line = nil
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func connectKeyboard(url string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnConnectDrive, URL: url},
	}}}
}

func transferOfferKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnTransferYes, CallbackData: cbPrefix + actTransferYes},
		{Text: btnTransferNo, CallbackData: cbPrefix + actTransferNo},
	}}}
}

func retryKeyboard(id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRetryBackup, CallbackData: cb(actRetry, id)},
	}}}
}

func openDriveKeyboard(doc docsbot.Document) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnOpenDrive, URL: driveURL(doc.DriveFileID)},
	}}}
}

func reconnectKeyboard(url string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnReconnect, URL: url},
	}}}
}

// cardText describes one document: name, category, date, and where it lives.
func cardText(doc docsbot.Document) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "📄 %s\n", doc.Title)
	fmt.Fprintf(b, "Категория: %s\n", categoryLabel(doc.Category))
	fmt.Fprintf(b, "Дата: %s\n", formatDate(doc.CreatedAt))
	if doc.InDrive() {
		b.WriteString("☁️ В Google Drive")
	} else {
		b.WriteString("📥 Хранится в Telegram — ещё не в Drive")
	}
	return b.String()
}

// cardKeyboard renders a document's actions. "Открыть в Drive" appears only once
// the file is backed up.
func cardKeyboard(doc docsbot.Document) *models.InlineKeyboardMarkup {
	top := []models.InlineKeyboardButton{{Text: btnSendFile, CallbackData: cb(actSend, doc.ID)}}
	if doc.InDrive() {
		top = append(top, models.InlineKeyboardButton{Text: btnOpenDrive, URL: driveURL(doc.DriveFileID)})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		top,
		{
			{Text: btnRename, CallbackData: cb(actRename, doc.ID)},
			{Text: btnChangeCat, CallbackData: cb(actChangeCat, doc.ID)},
			{Text: btnDelete, CallbackData: cb(actDeleteAsk, doc.ID)},
		},
	}}
}

// renderCatalog lists categories with counts; nil keyboard means an empty list.
func renderCatalog(c docsbot.Catalog) (string, *models.InlineKeyboardMarkup) {
	if len(c.Counts) == 0 {
		return textCatalogEmpty, nil
	}
	b := &strings.Builder{}
	b.WriteString("Твои документы:")
	if c.DriveConnected && c.NotInDrive > 0 {
		fmt.Fprintf(b, "\n\n📥 %d ещё не в Drive", c.NotInDrive)
	}
	var rows [][]models.InlineKeyboardButton
	for _, cc := range c.Counts {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text:         fmt.Sprintf("%s (%d)", categoryLabel(cc.Category), cc.Count),
			CallbackData: fmt.Sprintf("%s%s:%s:1", cbPrefix, actListCat, cc.Category),
		}})
	}
	if c.DriveConnected && c.NotInDrive > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnTransfer, CallbackData: cbPrefix + actTransfer}})
	}
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// renderSettings draws the /settings screen: Drive state, document counts, and
// the available actions. connectURL is the Google consent link; haveURL is false
// when it could not be built (then no connect/reconnect button is shown).
func renderSettings(v docsbot.SettingsView, connectURL string, haveURL bool) (string, *models.InlineKeyboardMarkup) {
	b := &strings.Builder{}
	if v.DriveConnected {
		b.WriteString("Google Drive: подключён ✅")
	} else {
		b.WriteString("Google Drive: не подключён")
	}
	fmt.Fprintf(b, "\n\nДокументов: %d", v.Total)
	if v.DriveConnected {
		fmt.Fprintf(b, "\n☁️ В Drive: %d", v.InDrive)
		fmt.Fprintf(b, "\n📥 Только в Telegram: %d", v.OnlyTelegram)
	}
	if v.Failed > 0 {
		fmt.Fprintf(b, "\n⚠️ Не выгрузились: %d", v.Failed)
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
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnDisconnect, CallbackData: cbPrefix + actDisconnect}})
	}
	// Retry only makes sense while Drive is connected; a disconnected retry would
	// re-fail every document and spam reconnect prompts.
	if v.DriveConnected && v.Failed > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: fmt.Sprintf("%s (%d)", btnRetryAll, v.Failed), CallbackData: cbPrefix + actRetryAll,
		}})
	}
	if v.Total > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnExportAll, CallbackData: cbPrefix + actExport}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnDeleteData, CallbackData: cbPrefix + actDeleteData}})
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// renderCategoryPage lists one page of a category, a send button per document.
func renderCategoryPage(p docsbot.CategoryPage) (string, *models.InlineKeyboardMarkup) {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s — %d", categoryLabel(p.Category), p.Total)
	var rows [][]models.InlineKeyboardButton
	for _, doc := range p.Docs {
		badge := ""
		if p.DriveConnected {
			if doc.InDrive() {
				badge = "☁️ "
			} else {
				badge = "📥 "
			}
		}
		fmt.Fprintf(b, "\n\n%s%s · %s", badge, doc.Title, formatDate(doc.CreatedAt))
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: "📤 " + truncate(doc.Title, 40), CallbackData: cb(actSend, doc.ID),
		}})
	}
	if nav := paginationRow(p); nav != nil {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnBackToCats, CallbackData: cbPrefix + actListBack}})
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// paginationRow shows ◀ page/pages ▶; arrows appear only where there is a page.
func paginationRow(p docsbot.CategoryPage) []models.InlineKeyboardButton {
	if p.Pages <= 1 {
		return nil
	}
	var row []models.InlineKeyboardButton
	if p.Page > 1 {
		row = append(row, models.InlineKeyboardButton{
			Text: "◀", CallbackData: fmt.Sprintf("%s%s:%s:%d", cbPrefix, actListCat, p.Category, p.Page-1),
		})
	}
	row = append(row, models.InlineKeyboardButton{
		Text: fmt.Sprintf("%d/%d", p.Page, p.Pages), CallbackData: cbPrefix + actNoop,
	})
	if p.Page < p.Pages {
		row = append(row, models.InlineKeyboardButton{
			Text: "▶", CallbackData: fmt.Sprintf("%s%s:%s:%d", cbPrefix, actListCat, p.Category, p.Page+1),
		})
	}
	return row
}

// renderFound lists search results; nil keyboard means nothing was found.
func renderFound(f docsbot.Found) (string, *models.InlineKeyboardMarkup) {
	if len(f.Docs) == 0 {
		return textNothingFound, nil
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "Нашёл (%d):", len(f.Docs))
	var rows [][]models.InlineKeyboardButton
	for _, doc := range f.Docs {
		fmt.Fprintf(b, "\n\n📄 %s · %s", doc.Title, categoryLabel(doc.Category))
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: "📤 " + truncate(doc.Title, 40), CallbackData: cb(actSend, doc.ID),
		}})
	}
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// incomingFile reads the file out of a message; ok is false for a non-file one.
func incomingFile(m *models.Message, user docsbot.UserID) (docsbot.IncomingFile, bool) {
	switch {
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1] // Telegram sorts sizes ascending
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindPhoto,
			FileID: p.FileID, FileUniqueID: p.FileUniqueID, Title: photoTitle(m),
		}, true
	case m.Document != nil:
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument,
			FileID: m.Document.FileID, FileUniqueID: m.Document.FileUniqueID, Title: documentTitle(m),
		}, true
	default:
		return docsbot.IncomingFile{}, false
	}
}

func documentTitle(m *models.Message) string {
	switch {
	case m.Document.FileName != "":
		return m.Document.FileName
	case m.Caption != "":
		return m.Caption
	default:
		return "Документ"
	}
}

func photoTitle(m *models.Message) string {
	if m.Caption != "" {
		return m.Caption
	}
	return "Фото"
}

func driveURL(fileID string) string {
	return "https://drive.google.com/file/d/" + fileID + "/view"
}

func formatDate(t time.Time) string {
	return t.Format("02.01.2006")
}

// truncate shortens s to n runes, adding an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
