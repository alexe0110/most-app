package telegram

import (
	"fmt"
	"html"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// cbPrefix tags every inline button the docs feature owns, keeping it clear of
// the /convert buttons ("conv:").
const cbPrefix = "d:"

// Callback actions. Data is "d:<action>" or "d:<action>:<docID>[:<arg>]".
const (
	actCategory      = "cat" // pick a category for a new document
	actCategoryGroup = "ga"  // pick a category for a whole album: d:ga:<docID>:<slug>
	actGroupPick     = "gpk" // album choice -> category picker: d:gpk:<docID>
	actSetCategory   = "sc"  // change an existing document's category
	actChangeCat     = "chc" // show the category picker for a card
	actSend          = "snd" // resend the original file
	actRename        = "rn"  // ask for a new title: the card message becomes the prompt
	actRenameCancel  = "rnc" // cancel the rename prompt: d:rnc:<docID>
	actDeleteAsk     = "del" // ask to confirm deletion
	actDeleteYes     = "dy"  // confirm deletion
	actDeleteNo      = "dn"  // cancel deletion
	actListCat       = "lc"  // open a category page: d:lc:<slug>:<page>
	actListBack      = "lb"  // back to the category list
	actTransfer      = "tr"  // transfer saved documents to Drive
	actTransferYes   = "try" // confirm the transfer offer
	actTransferNo    = "trn" // decline the transfer offer
	actRetry         = "rt"  // retry a failed backup: d:rt:<docID>
	actRetrySync     = "rs"  // retry a failed cloud sync: d:rs:<docID>
	actDisconnect    = "dc"  // disconnect Google Drive
	actRetryAll      = "rta" // retry every failed backup
	actExport        = "exp" // resend all documents from Telegram
	actDeleteData    = "dd"  // start the delete-all-data confirmation
	actRetryBatch    = "rsm" // retry failed docs in a status message: d:rsm:<smID>
	actOpenCard      = "oc"  // open a card: d:oc:<docID>:<l|s>[:<cat>:<page>]
	actSearchBack    = "sb"  // re-run the last search
	actInline        = "inl" // switch inline mode: d:inl:<1|0> is the wanted state
	actNoop          = "noop"
)

// cb builds "d:<action>:<docID>".
func cb(action string, id docsbot.DocumentID) string {
	return fmt.Sprintf("%s%s:%s", cbPrefix, action, id)
}

// categoryRows lays category buttons out two per row, with «Другое» alone in the
// last row.
func categoryRows(cats []docsbot.Category, button func(docsbot.Category) models.InlineKeyboardButton) [][]models.InlineKeyboardButton {
	var rows [][]models.InlineKeyboardButton
	var line []models.InlineKeyboardButton
	var other []models.InlineKeyboardButton
	for _, c := range cats {
		if c == docsbot.CatOther {
			other = []models.InlineKeyboardButton{button(c)}
			continue
		}
		line = append(line, button(c))
		if len(line) == 2 {
			rows = append(rows, line)
			line = nil
		}
	}
	if len(line) > 0 {
		rows = append(rows, line)
	}
	if other != nil {
		rows = append(rows, other)
	}
	return rows
}

// categoryKeyboard offers every category, two per row. action is actCategory for
// a new document or actSetCategory when changing one.
func categoryKeyboard(action string, id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	rows := categoryRows(docsbot.AllCategories, func(c docsbot.Category) models.InlineKeyboardButton {
		return models.InlineKeyboardButton{
			Text:         c.Label(),
			CallbackData: fmt.Sprintf("%s%s:%s:%s", cbPrefix, action, id, c),
		}
	})
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// pickerKeyboard is the category picker of a single new file. A file the
// converter can handle gets an extra row opening the convert menu.
func pickerKeyboard(doc docsbot.Document) *models.InlineKeyboardMarkup {
	kb := categoryKeyboard(actCategory, doc.ID)
	if convertible(doc) {
		label := btnConvertPicker
		if isPDF(doc) {
			label = btnConvertPickerPDF
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, convertRow(doc.ID, label))
	}
	return kb
}

// albumChoiceKeyboard offers an album as one PDF or saved file by file.
func albumChoiceKeyboard(token string, pickDoc docsbot.DocumentID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: btnAlbumPDF, CallbackData: cbAlbumPrefix + token}},
		{{Text: btnAlbumEach, CallbackData: cb(actGroupPick, pickDoc)}},
	}}
}

// convertRow is a one-button row opening the convert menu of a document.
func convertRow(id docsbot.DocumentID, label string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: label, CallbackData: cbDocPrefix + string(id)}}
}

// renameCancelKeyboard is the single "Отмена" button under the rename prompt.
func renameCancelKeyboard(id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRenameCancel, CallbackData: cb(actRenameCancel, id)},
	}}}
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

// cardText describes one document as HTML (every dynamic part is escaped):
//
//	📄 <b>ВНЖ</b>
//	🗂 Паспорт и ID · PDF · 3 стр. · 194 КБ
//	📅 10.11.2026
//	☁️ Google Drive · копия в Telegram
func cardText(doc docsbot.Document) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s <b>%s</b>\n", typeIcon(doc), html.EscapeString(doc.Title))
	b.WriteString("🗂 " + html.EscapeString(strings.Join(cardFacts(doc), " · ")) + "\n")
	b.WriteString("📅 " + formatDate(doc.CreatedAt) + "\n")
	b.WriteString(html.EscapeString(storageLine(doc)))
	return b.String()
}

// cardFacts are the details after the category: type, pixel size, pages, file size; unknown ones are left out.
func cardFacts(doc docsbot.Document) []string {
	facts := []string{doc.Category.Label()}
	if t := fileType(doc); t != "" {
		facts = append(facts, t)
	}
	if doc.Width > 0 && doc.Height > 0 {
		facts = append(facts, fmt.Sprintf("%d×%d", doc.Width, doc.Height))
	}
	if doc.PageCount > 0 {
		facts = append(facts, fmt.Sprintf(textFactPages, doc.PageCount))
	}
	if sz := humanSize(doc.FileSize); sz != "" {
		facts = append(facts, sz)
	}
	return facts
}

// storageLine says where the document lives right now (plain text).
func storageLine(doc docsbot.Document) string {
	switch {
	case doc.BackupStatus == docsbot.BackupPending || doc.BackupStatus == docsbot.BackupUploading:
		return textStoragePending
	case doc.InDrive() && doc.SyncStatus == docsbot.SyncPending:
		return textStorageSyncing
	case doc.InDrive() && doc.SyncStatus == docsbot.SyncFailed && doc.SyncReason == docsbot.SyncReasonRevoked:
		return textStorageSyncRevoked
	case doc.InDrive() && doc.SyncStatus == docsbot.SyncFailed:
		return textStorageSyncFailed
	case doc.InDrive() && doc.SyncStatus == docsbot.SyncMissing:
		return textStorageMissing
	case doc.InDrive():
		return textStorageInDrive
	case doc.BackupStatus == docsbot.BackupFailed:
		return textStorageFailed
	case doc.BackupStatus == docsbot.BackupTooLarge:
		return textStorageTooLarge
	default:
		return textStorageTelegramOnly
	}
}

// mimeTypeLabels names the content types a card shows, by MIME.
var mimeTypeLabels = map[string]string{
	"application/pdf":    "PDF",
	"image/jpeg":         "JPG",
	"image/jpg":          "JPG",
	"image/png":          "PNG",
	"image/webp":         "WEBP",
	"image/heic":         "HEIC",
	"image/heif":         "HEIC",
	"application/msword": "DOC",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "DOCX",
	"application/vnd.ms-excel": "XLS",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "XLSX",
	"application/vnd.ms-powerpoint":                                             "PPT",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "PPTX",
	"text/plain":      "TXT",
	"application/zip": "ZIP",
}

// fileType is a short label of the file kind, decided by its MIME type only (a
// title is free text); "" when unknown. A photo has no MIME and reads "Фото".
func fileType(doc docsbot.Document) string {
	mime := strings.ToLower(strings.TrimSpace(strings.SplitN(doc.MIME, ";", 2)[0]))
	if label, ok := mimeTypeLabels[mime]; ok {
		return label
	}
	if doc.Kind == docsbot.KindPhoto {
		return textFactPhoto
	}
	return ""
}

// humanSize formats a byte count in KB or MB; "" when the size is unknown.
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	if kb := (n + 1023) / 1024; kb < 1024 {
		return fmt.Sprintf(textSizeKB, kb)
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".", ",") + textSizeMBSuffix
}

// folderURL is the Drive web link to a folder.
func folderURL(id string) string { return "https://drive.google.com/drive/folders/" + id }

// cardKeyboard renders a document's actions. "Открыть в Drive" appears only once
// the file is backed up, the folder button only when the category folder exists.
func cardKeyboard(doc docsbot.Document, folderID, orig string) *models.InlineKeyboardMarkup {
	return cardKeyboardWith(doc, folderID, orig, "")
}

// revoked reports a failed sync caused by revoked Drive access: retrying cannot
// help until the user reconnects.
func revoked(doc docsbot.Document) bool {
	return doc.InDrive() && doc.SyncStatus == docsbot.SyncFailed && doc.SyncReason == docsbot.SyncReasonRevoked
}

// cardKeyboardWith is cardKeyboard with the Google consent link, which replaces
// "Повторить" on a card whose sync failed because access was revoked; without a
// link such a card has no retry button.
func cardKeyboardWith(doc docsbot.Document, folderID, orig, reconnectURL string) *models.InlineKeyboardMarkup {
	// At most two buttons per row, short labels: more are cut off on narrow screens.
	top := []models.InlineKeyboardButton{{Text: btnCardSend, CallbackData: cb(actSend, doc.ID)}}
	if doc.InDrive() && doc.SyncStatus != docsbot.SyncMissing {
		top = append(top, models.InlineKeyboardButton{Text: btnCardOpen, URL: driveURL(doc.DriveFileID)})
	}
	rows := [][]models.InlineKeyboardButton{top}
	if doc.InDrive() {
		switch doc.SyncStatus {
		case docsbot.SyncFailed:
			switch {
			case !revoked(doc):
				rows = append(rows, []models.InlineKeyboardButton{{Text: btnRetryBackup, CallbackData: cb(actRetrySync, doc.ID)}})
			case reconnectURL != "":
				rows = append(rows, []models.InlineKeyboardButton{{Text: btnReconnect, URL: reconnectURL}})
			}
		case docsbot.SyncMissing:
			rows = append(rows, []models.InlineKeyboardButton{{Text: btnReupload, CallbackData: cb(actRetry, doc.ID)}})
		}
	}
	if folderID != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnOpenFolder, URL: folderURL(folderID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: btnRename, CallbackData: cb(actRename, doc.ID)},
		{Text: btnChangeCat, CallbackData: cb(actChangeCat, doc.ID)},
	})
	var last []models.InlineKeyboardButton
	if convertible(doc) {
		last = append(last, convertRow(doc.ID, btnConvertCard)...)
	}
	last = append(last, models.InlineKeyboardButton{Text: btnDelete, CallbackData: cb(actDeleteAsk, doc.ID)})
	rows = append(rows, last)
	if back := backFromOrig(orig); back != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnBack, CallbackData: back}})
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// backFromOrig is the callback of the card's "Назад" button for an origin token:
// the category page for "l:<cat>:<page>", the last search for "s", "" otherwise.
func backFromOrig(orig string) string {
	switch {
	case strings.HasPrefix(orig, "l:"):
		return cbPrefix + actListCat + strings.TrimPrefix(orig, "l")
	case orig == "s":
		return cbPrefix + actSearchBack
	}
	return ""
}

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
			CallbackData: fmt.Sprintf("%s%s:%s:1", cbPrefix, actListCat, cat),
		}
	})
	if c.DriveConnected && c.NotInDrive > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnTransfer, CallbackData: cbPrefix + actTransfer}})
	}
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// exportFirstKeyboard is the button under the delete-data prompt: get everything
// back first, then confirm.
func exportFirstKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnExportAll, CallbackData: cbPrefix + actExport},
	}}}
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
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnDisconnect, CallbackData: cbPrefix + actDisconnect}})
	}
	// Retry only makes sense while Drive is connected; a disconnected retry would
	// re-fail every document and spam reconnect prompts.
	if v.DriveConnected && v.Failed > 0 {
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: fmt.Sprintf("%s (%d)", btnRetryAll, v.Failed), CallbackData: cbPrefix + actRetryAll,
		}})
	}
	rows = append(rows, inlineToggleRow(v.InlineEnabled))
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnDeleteData, CallbackData: cbPrefix + actDeleteData}})
	return b.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// inlineToggleRow is the «send from any chat» switch: it shows the current state,
// a tap asks for the opposite one.
func inlineToggleRow(enabled bool) []models.InlineKeyboardButton {
	if enabled {
		return []models.InlineKeyboardButton{{Text: btnInlineOn, CallbackData: cbPrefix + actInline + ":0"}}
	}
	return []models.InlineKeyboardButton{{Text: btnInlineOff, CallbackData: cbPrefix + actInline + ":1"}}
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
			CallbackData: fmt.Sprintf("%s%s:%s:l:%s:%d", cbPrefix, actOpenCard, doc.ID, p.Category, p.Page),
		}})
	}
	if nav := paginationRow(p); nav != nil {
		rows = append(rows, nav)
	}
	if p.FolderID != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: btnOpenCategoryFolder, URL: folderURL(p.FolderID)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: btnBackToCats, CallbackData: cbPrefix + actListBack}})
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
			CallbackData: fmt.Sprintf("%s%s:%s:s", cbPrefix, actOpenCard, doc.ID),
		}})
	}
	return fmt.Sprintf(textFoundTitle, len(f.Docs)), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// incomingFile reads the file out of a message; ok is false for a non-file one.
func incomingFile(m *models.Message, user docsbot.UserID) (docsbot.IncomingFile, bool) {
	switch {
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1] // Telegram sorts sizes ascending
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindPhoto,
			FileID: p.FileID, FileUniqueID: p.FileUniqueID, Title: photoTitle(m),
			FileSize: int64(p.FileSize), MIME: "", MediaGroupID: m.MediaGroupID,
			Width: p.Width, Height: p.Height,
		}, true
	case m.Document != nil:
		return docsbot.IncomingFile{
			UserID: user, Kind: docsbot.KindDocument,
			FileID: m.Document.FileID, FileUniqueID: m.Document.FileUniqueID, Title: documentTitle(m),
			FileSize: m.Document.FileSize, MIME: m.Document.MimeType, MediaGroupID: m.MediaGroupID,
		}, true
	default:
		return docsbot.IncomingFile{}, false
	}
}

// documentTitle is the file name without its extension, or the caption; "" when neither exists (the domain
// then assigns a default name).
func documentTitle(m *models.Message) string {
	if m.Document.FileName != "" {
		return docsbot.TrimExt(m.Document.FileName)
	}
	return m.Caption
}

// photoTitle is the caption, "" when absent.
func photoTitle(m *models.Message) string {
	return m.Caption
}

func driveURL(fileID string) string {
	return "https://drive.google.com/file/d/" + fileID + "/view"
}

func formatDate(t time.Time) string {
	return t.Format("02.01.2006")
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

// statusTextLimit is Telegram's cap on a message text. It is measured in UTF-16
// code units after entity parsing; the HTML length below is a safe upper bound.
const statusTextLimit = 4096

// statusBarSegments is the width of the progress bar of a batch status message.
const statusBarSegments = 7

// statusTitleCaps are the per-category title counts tried in turn until the
// message fits statusTextLimit; 0 leaves only «…и ещё N».
var statusTitleCaps = []int{10, 5, 3, 1, 0}

// renderStatusMessage draws a status message (HTML) according to its kind: a
// live card for one document, or, for a batch or a transfer, one common view: a
// header with a progress bar while uploads run, an error block for what did not
// get to Drive, and a block per category with the titles that did. The blocks
// are shown while the batch is still running, too.
func renderStatusMessage(sm docsbot.StatusMessage, lines []docsbot.Document, folderByCat map[docsbot.Category]string) (string, *models.InlineKeyboardMarkup) {
	if sm.Kind == docsbot.StatusSingle && len(lines) == 1 {
		return renderSingleStatus(sm, lines[0], folderByCat)
	}
	st := tallyStatus(lines)
	var text string
	for _, limit := range statusTitleCaps {
		text = statusText(st, folderByCat, limit)
		if utf16Len(text) <= statusTextLimit {
			break
		}
	}
	return text, statusKeyboard(sm.ID, st)
}

// statusTally is what a batch status message is drawn from.
type statusTally struct {
	total, done, backedUp, failed int
	// titles holds the titles of backed-up documents per category (unknown
	// categories fold into «Другое»), in upload order.
	titles map[docsbot.Category][]string
	// notDone counts documents that did not reach Drive, by reason.
	notDone map[string]int
}

// reasonTooLarge is the statusTally.notDone key of files above the size limit.
const reasonTooLarge = "too_large"

func tallyStatus(lines []docsbot.Document) statusTally {
	st := statusTally{total: len(lines), titles: map[docsbot.Category][]string{}, notDone: map[string]int{}}
	for _, d := range lines {
		if d.BackupStatus.Terminal() {
			st.done++
		}
		switch d.BackupStatus {
		case docsbot.BackupBackedUp:
			st.backedUp++
			cat := d.Category
			if !cat.Valid() {
				cat = docsbot.CatOther
			}
			st.titles[cat] = append(st.titles[cat], html.EscapeString(truncateMiddle(d.Title, listTitleRunes)))
		case docsbot.BackupFailed:
			st.failed++
			st.notDone[string(failReason(d.BackupReason))]++
		case docsbot.BackupTooLarge:
			st.notDone[reasonTooLarge]++
		}
	}
	return st
}

// failReason normalizes a stored backup reason; unknown and empty read as unavailable.
func failReason(r docsbot.BackupReason) docsbot.BackupReason {
	switch r {
	case docsbot.BackupReasonQuota, docsbot.BackupReasonRevoked:
		return r
	}
	return docsbot.BackupReasonUnavailable
}

// statusReasons lists the reasons in display order with their texts.
var statusReasons = []struct{ key, text string }{
	{string(docsbot.BackupReasonQuota), textReasonQuota},
	{string(docsbot.BackupReasonRevoked), textReasonRevoked},
	{string(docsbot.BackupReasonUnavailable), textReasonUnavailable},
	{reasonTooLarge, fmt.Sprintf(textReasonTooLarge, docsbot.MaxDriveBytes>>20)},
}

// statusText assembles the message with at most titleCap titles per category.
func statusText(st statusTally, folderByCat map[docsbot.Category]string, titleCap int) string {
	var parts []string
	switch {
	case st.done < st.total || st.total == 0:
		parts = append(parts, fmt.Sprintf(textStatusProgress, st.done, st.total)+"\n"+progressBar(st.done, st.total))
	case st.backedUp > 0:
		parts = append(parts, fmt.Sprintf(textStatusDone, st.backedUp, plural(st.backedUp, pluralDocumentOne, pluralDocumentFew, pluralDocumentMany)))
	}
	if block := errorBlock(st); block != "" {
		parts = append(parts, block)
	}
	for _, cat := range docsbot.AllCategories {
		if titles := st.titles[cat]; len(titles) > 0 {
			parts = append(parts, categoryBlock(cat, titles, folderByCat[cat], titleCap))
		}
	}
	return strings.Join(parts, "\n\n")
}

// progressBar draws done/total as filled and empty segments; it is full only when done == total.
func progressBar(done, total int) string {
	filled := 0
	if total > 0 {
		filled = done * statusBarSegments / total
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", statusBarSegments-filled)
}

// errorBlock lists the documents that did not reach Drive: one line for a single
// reason, a bulleted list for several. Empty when everything went fine.
func errorBlock(st statusTally) string {
	n := 0
	for _, c := range st.notDone {
		n += c
	}
	if n == 0 {
		return ""
	}
	var lines []string
	single := ""
	for _, r := range statusReasons {
		if c := st.notDone[r.key]; c > 0 {
			lines = append(lines, fmt.Sprintf(textStatusReasonLine, r.text, c))
			single = r.text
		}
	}
	if len(lines) == 1 {
		return fmt.Sprintf(textStatusNotDone, n, single)
	}
	return fmt.Sprintf(textStatusNotDoneList, n) + "\n" + strings.Join(lines, "\n")
}

// categoryBlock is «📁 Label · K» (the label links to the Drive folder when it
// is known) over a line of titles, cut to titleCap with «…и ещё N».
func categoryBlock(cat docsbot.Category, titles []string, folderID string, titleCap int) string {
	label := html.EscapeString(cat.Label())
	if folderID != "" {
		label = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(folderURL(folderID)), label)
	}
	head := fmt.Sprintf(textStatusCategory, label, len(titles))
	shown := titles
	if len(shown) > titleCap {
		shown = shown[:titleCap]
	}
	line := strings.Join(shown, ", ")
	if hidden := len(titles) - len(shown); hidden > 0 {
		more := fmt.Sprintf(textStatusMore, hidden)
		if line != "" {
			line += ", "
		}
		line += more
	}
	return head + "\n" + line
}

// statusKeyboard has «Повторить (R)» while some uploads failed and «Мои
// документы» once the batch is over; nil when neither applies.
func statusKeyboard(id docsbot.StatusMessageID, st statusTally) *models.InlineKeyboardMarkup {
	var row []models.InlineKeyboardButton
	if st.failed > 0 {
		row = append(row, models.InlineKeyboardButton{
			Text:         fmt.Sprintf("%s (%d)", btnRetryBackup, st.failed),
			CallbackData: cbPrefix + actRetryBatch + ":" + string(id),
		})
	}
	if st.done == st.total && st.total > 0 {
		row = append(row, models.InlineKeyboardButton{Text: btnMyDocs, CallbackData: cbPrefix + actListBack})
	}
	if len(row) == 0 {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

// utf16Len is the length of s in UTF-16 code units, the way Telegram counts text.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// renderSingleStatus draws one uploading document as a card with a live storage line.
func renderSingleStatus(sm docsbot.StatusMessage, doc docsbot.Document, folderByCat map[docsbot.Category]string) (string, *models.InlineKeyboardMarkup) {
	kb := cardKeyboard(doc, folderByCat[doc.Category], "")
	if doc.BackupStatus == docsbot.BackupFailed {
		kb.InlineKeyboard = append(kb.InlineKeyboard, retryBatchKeyboard(sm.ID).InlineKeyboard...)
	}
	return cardText(doc), kb
}

func retryBatchKeyboard(id docsbot.StatusMessageID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRetryBackup, CallbackData: cbPrefix + actRetryBatch + ":" + string(id)},
	}}}
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
