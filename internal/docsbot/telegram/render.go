package telegram

import (
	"fmt"
	"html"
	"path/filepath"
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
	actTransfer      = "tr"  // transfer saved documents to Drive (#11)
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

// cardText describes one document: name, category, date, and where it lives.
func cardText(doc docsbot.Document) string { return cardBody(doc.Title, doc) }

// cardBody is cardText with a caller-prepared title (HTML-escaped for HTML messages).
func cardBody(title string, doc docsbot.Document) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "📄 %s\n", title)
	fmt.Fprintf(b, "Категория: %s\n", doc.Category.Label())
	fmt.Fprintf(b, "Добавлен: %s", formatDate(doc.CreatedAt))
	if t := fileType(doc); t != "" {
		b.WriteString(" · " + t)
	}
	if sz := humanSize(doc.FileSize); sz != "" {
		b.WriteString(" · " + sz)
	}
	b.WriteString("\n")
	b.WriteString(storageLine(doc))
	return b.String()
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

// fileType is a short label of the file kind; "" when unknown.
func fileType(doc docsbot.Document) string {
	switch doc.MIME {
	case "application/pdf":
		return "PDF"
	case "image/jpeg", "image/jpg":
		return "JPG"
	case "image/png":
		return "PNG"
	}
	if ext := strings.TrimPrefix(filepath.Ext(doc.Title), "."); looksLikeExt(ext) {
		return strings.ToUpper(ext)
	}
	if doc.Kind == docsbot.KindPhoto {
		return "Фото"
	}
	return ""
}

// looksLikeExt reports whether s is plausibly a file extension: 1-5 ASCII
// letters or digits with at least one letter (so a year like "2026" is not one).
func looksLikeExt(s string) bool {
	if len(s) < 1 || len(s) > 5 {
		return false
	}
	letter := false
	for _, r := range s {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !isLetter && (r < '0' || r > '9') {
			return false
		}
		letter = letter || isLetter
	}
	return letter
}

// humanSize formats a byte count in KB or MB; "" when the size is unknown.
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	if kb := (n + 1023) / 1024; kb < 1024 {
		return fmt.Sprintf("%d КБ", kb)
	}
	return strings.ReplaceAll(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".", ",") + " МБ"
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
	// At most two buttons per row: more are cut off on narrow screens.
	top := []models.InlineKeyboardButton{{Text: btnSendFile, CallbackData: cb(actSend, doc.ID)}}
	if doc.InDrive() && doc.SyncStatus != docsbot.SyncMissing {
		top = append(top, models.InlineKeyboardButton{Text: btnOpenDrive, URL: driveURL(doc.DriveFileID)})
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
	last := []models.InlineKeyboardButton{{Text: btnDelete, CallbackData: cb(actDeleteAsk, doc.ID)}}
	if convertible(doc) {
		last = append(last, convertRow(doc.ID, btnConvertCard)...)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{
			{Text: btnRename, CallbackData: cb(actRename, doc.ID)},
			{Text: btnChangeCat, CallbackData: cb(actChangeCat, doc.ID)},
		},
		last,
	)
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
	b.WriteString("Твои документы:")
	if c.DriveConnected && c.NotInDrive > 0 {
		fmt.Fprintf(b, "\n\n📥 %d ещё не в Drive", c.NotInDrive)
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
	return fmt.Sprintf("Нашёл (%d):", len(f.Docs)), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
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
	if n < 3 {
		return truncate(s, n)
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

// statusMaxLines caps the document lines in one status message so it stays well
// below Telegram's 4096-character limit.
const statusMaxLines = 20

// statusNameLimit shortens long document names inside status lines.
const statusNameLimit = 40

// renderStatusMessage draws a status message (HTML) according to its kind: a
// live card for one document, or a counter with one line per document.
func renderStatusMessage(sm docsbot.StatusMessage, lines []docsbot.Document, folderByCat map[docsbot.Category]string) (string, *models.InlineKeyboardMarkup) {
	if sm.Kind == docsbot.StatusSingle && len(lines) == 1 {
		return renderSingleStatus(sm, lines[0], folderByCat)
	}
	done, backedUp, failed := 0, 0, false
	for _, d := range lines {
		if d.BackupStatus.Terminal() {
			done++
		}
		if d.InDrive() {
			backedUp++
		}
		if d.BackupStatus == docsbot.BackupFailed {
			failed = true
		}
	}
	header := textStatusHeader
	if sm.Kind == docsbot.StatusTransfer {
		header = textTransferHeader
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, header, done, len(lines))
	for i, d := range lines {
		if i == statusMaxLines {
			fmt.Fprintf(b, "\n"+textStatusMore, len(lines)-statusMaxLines)
			break
		}
		b.WriteString("\n")
		b.WriteString(statusLine(d, folderByCat))
	}
	if sm.Kind == docsbot.StatusTransfer {
		if done == len(lines) {
			b.WriteString("\n\n" + fmt.Sprintf(textTransferAllDone, backedUp))
		}
	} else {
		b.WriteString("\n\n" + textStatusListHint)
	}
	if !failed {
		return b.String(), nil
	}
	return b.String(), retryBatchKeyboard(sm.ID)
}

// renderSingleStatus draws one uploading document as a card with a live storage line.
func renderSingleStatus(sm docsbot.StatusMessage, doc docsbot.Document, folderByCat map[docsbot.Category]string) (string, *models.InlineKeyboardMarkup) {
	kb := cardKeyboard(doc, folderByCat[doc.Category], "")
	if doc.BackupStatus == docsbot.BackupFailed {
		kb.InlineKeyboard = append(kb.InlineKeyboard, retryBatchKeyboard(sm.ID).InlineKeyboard...)
	}
	return cardBody(html.EscapeString(doc.Title), doc), kb
}

func retryBatchKeyboard(id docsbot.StatusMessageID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRetryBackup, CallbackData: cbPrefix + actRetryBatch + ":" + string(id)},
	}}}
}

func statusLine(d docsbot.Document, folderByCat map[docsbot.Category]string) string {
	name := html.EscapeString(truncate(d.Title, statusNameLimit))
	switch d.BackupStatus {
	case docsbot.BackupBackedUp:
		line := fmt.Sprintf("✅ %s · %s · <a href=%q>файл</a>", name, d.Category.Label(), driveURL(d.DriveFileID))
		if id := folderByCat[d.Category]; id != "" {
			line += fmt.Sprintf(" <a href=%q>папка</a>", folderURL(id))
		}
		return line
	case docsbot.BackupFailed:
		return "❌ " + name + " · не удалось"
	case docsbot.BackupTooLarge:
		return "📥 " + name + " · слишком большой для Drive"
	default:
		return fmt.Sprintf("⏳ %s · %s", name, d.Category.Label())
	}
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
