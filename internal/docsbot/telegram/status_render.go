package telegram

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
