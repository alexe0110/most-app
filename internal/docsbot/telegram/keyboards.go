package telegram

import (
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
			CallbackData: docCB(action, id, string(c)),
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
		{{Text: btnAlbumPDF, CallbackData: convCB(convActAlbum, token)}},
		{{Text: btnAlbumEach, CallbackData: docCB(actGroupPick, pickDoc)}},
	}}
}

// convertRow is a one-button row opening the convert menu of a document.
func convertRow(id docsbot.DocumentID, label string) []models.InlineKeyboardButton {
	return []models.InlineKeyboardButton{{Text: label, CallbackData: convDocCB(convActDoc, id)}}
}

// renameCancelKeyboard is the single "Отмена" button under the rename prompt.
func renameCancelKeyboard(id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRenameCancel, CallbackData: docCB(actRenameCancel, id)},
	}}}
}

func connectKeyboard(url string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnConnectDrive, URL: url},
	}}}
}

func transferOfferKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnTransferYes, CallbackData: docCB(actTransferYes, "")},
		{Text: btnTransferNo, CallbackData: docCB(actTransferNo, "")},
	}}}
}

func retryKeyboard(id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnRetryBackup, CallbackData: docCB(actRetry, id)},
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

// exportFirstKeyboard is the button under the delete-data prompt: get everything
// back first, then confirm.
func exportFirstKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnExportAll, CallbackData: docCB(actExport, "")},
	}}}
}

// inlineToggleRow is the «send from any chat» switch: it shows the current state,
// a tap asks for the opposite one.
func inlineToggleRow(enabled bool) []models.InlineKeyboardButton {
	if enabled {
		return []models.InlineKeyboardButton{{Text: btnInlineOn, CallbackData: docCB(actInline, "", "0")}}
	}
	return []models.InlineKeyboardButton{{Text: btnInlineOff, CallbackData: docCB(actInline, "", "1")}}
}
