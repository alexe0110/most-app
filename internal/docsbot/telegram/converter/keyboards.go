package converter

import (
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func actionKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnPDF, CallbackData: cbPDF},
			{Text: btnCompress, CallbackData: cbCompress},
		},
		{{Text: btnPDFActions, CallbackData: cbPDFActions}},
	}}
}

func pdfKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnDone, CallbackData: cbDone},
		{Text: btnCancel, CallbackData: cbCancel},
	}}}
}

func limitKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnLimit1MB, CallbackData: cbLimit1},
			{Text: btnLimit2MB, CallbackData: cbLimit2},
			{Text: btnLimit5MB, CallbackData: cbLimit5},
		},
		{
			{Text: btnLimitCustom, CallbackData: cbLimitCustom},
			{Text: btnCancel, CallbackData: cbCancel},
		},
	}}
}

func formatKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnJPEG, CallbackData: cbFmtJPEG},
		{Text: btnPNG, CallbackData: cbFmtPNG},
	}}}
}

func cancelKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnCancel, CallbackData: cbCancel},
	}}}
}

// quickCB is the callback data of a quick operation on a stored document.
func quickCB(id docsbot.DocumentID, op string) string { return convDocCB(convActQuick, id, op) }

// quickKeyboard is the menu of an image; a WEBP also gets a plain conversion to JPG.
func quickKeyboard(id docsbot.DocumentID, webp bool) *models.InlineKeyboardMarkup {
	second := []models.InlineKeyboardButton{{Text: btnQuickPDF, CallbackData: quickCB(id, opPDF)}}
	if webp {
		second = append(second, models.InlineKeyboardButton{Text: btnQuickJPG, CallbackData: quickCB(id, opJPG)})
	}
	second = append(second, models.InlineKeyboardButton{Text: btnQuickCancel, CallbackData: cbMenuCancel})
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnQuick1MB, CallbackData: quickCB(id, opCompress1)},
			{Text: btnQuick2MB, CallbackData: quickCB(id, opCompress2)},
			{Text: btnQuick5MB, CallbackData: quickCB(id, opCompress5)},
		},
		second,
	}}
}

// saveKeyboard is the result's keyboard. A result made from a stored document
// (replace) can also replace that document's file.
func saveKeyboard(token string, replace bool) *models.InlineKeyboardMarkup {
	if replace {
		return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: btnSaveAsNew, CallbackData: convCB(convActSave, token)}},
			{{Text: btnReplaceOriginal, CallbackData: convCB(convActRepAsk, token)}},
			{{Text: btnSkipResult, CallbackData: convCB(convActSkip, token)}},
		}}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: btnSaveResult, CallbackData: convCB(convActSave, token)}},
		{{Text: btnSkipResult, CallbackData: convCB(convActSkip, token)}},
	}}
}

// replaceKeyboard confirms replacing the original.
func replaceKeyboard(token string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnReplaceYes, CallbackData: convCB(convActRepYes, token)},
		{Text: btnReplaceNo, CallbackData: convCB(convActRepNo, token)},
	}}}
}

// quickLimit maps a compress operation of the menu to its size in bytes.
func quickLimit(op string) (int, bool) {
	switch op {
	case opCompress1:
		return 1 << 20, true
	case opCompress2:
		return 2 << 20, true
	case opCompress5:
		return 5 << 20, true
	default:
		return 0, false
	}
}

// infoKeyboard is the buttons of the info screen. «Назад» removes the screen: the
// message it was opened from (the picker) stays and is still live.
func infoKeyboard(id docsbot.DocumentID, canClean bool) *models.InlineKeyboardMarkup {
	var row []models.InlineKeyboardButton
	if canClean {
		row = append(row, models.InlineKeyboardButton{Text: btnClean, CallbackData: convDocCB(convActClean, id)})
	}
	row = append(row, models.InlineKeyboardButton{Text: btnInfoBack, CallbackData: cbMenuCancel})
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

// cleanedKeyboard asks which of the two files to keep.
func cleanedKeyboard(token string) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: btnKeepClean, CallbackData: convCB(convActKeepClean, token)}},
		{{Text: btnKeepOriginal, CallbackData: convCB(convActKeepOrig, token)}},
	}}
}
