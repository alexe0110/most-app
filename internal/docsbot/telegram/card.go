package telegram

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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

func driveURL(fileID string) string {
	return "https://drive.google.com/file/d/" + fileID + "/view"
}

func formatDate(t time.Time) string {
	return t.Format("02.01.2006")
}
