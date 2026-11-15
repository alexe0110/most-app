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

// statusLineCaps are the per-category line counts tried in turn until the message
// fits statusTextLimit; 0 leaves only the «и ещё N документов» line.
var statusLineCaps = []int{10, 5, 3, 1, 0}

// renderStatusMessage draws a status message (HTML) according to its kind: a
// live card for one document, or, for a batch or a transfer, one common view: a
// header (with a progress bar while uploads run) and, per category, a line for
// each document with its own state. The lines are shown while the batch is still
// running, too.
func renderStatusMessage(sm docsbot.StatusMessage, lines []docsbot.Document, folderByCat map[docsbot.Category]string) (string, *models.InlineKeyboardMarkup) {
	if sm.Kind == docsbot.StatusSingle && len(lines) == 1 {
		return renderSingleStatus(sm, lines[0], folderByCat)
	}
	st := tallyStatus(lines)
	var text string
	for _, limit := range statusLineCaps {
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
	// notDone counts documents that did not reach Drive (failed or too large).
	notDone int
	// quota is set when some upload failed for lack of space on Drive.
	quota bool
	// lines holds the rendered line of every document per category (unknown
	// categories fold into «Другое»), in upload order.
	lines map[docsbot.Category][]string
}

func tallyStatus(docs []docsbot.Document) statusTally {
	st := statusTally{total: len(docs), lines: map[docsbot.Category][]string{}}
	for _, d := range docs {
		if d.BackupStatus.Terminal() {
			st.done++
		}
		switch d.BackupStatus {
		case docsbot.BackupBackedUp:
			st.backedUp++
		case docsbot.BackupFailed:
			st.failed++
			st.notDone++
			st.quota = st.quota || failReason(d.BackupReason) == docsbot.BackupReasonQuota
		case docsbot.BackupTooLarge:
			st.notDone++
		}
		cat := d.Category
		if !cat.Valid() {
			cat = docsbot.CatOther
		}
		st.lines[cat] = append(st.lines[cat], statusLine(d))
	}
	return st
}

// statusLine is «• <state> <title>» of one document; a document that did not
// reach Drive says why.
func statusLine(d docsbot.Document) string {
	title := html.EscapeString(truncateMiddle(d.Title, listTitleRunes))
	switch d.BackupStatus {
	case docsbot.BackupBackedUp:
		return fmt.Sprintf(textStatusLineDone, title)
	case docsbot.BackupFailed, docsbot.BackupTooLarge:
		return fmt.Sprintf(textStatusLineFailed, title, failureText(d))
	default:
		return fmt.Sprintf(textStatusLinePending, title)
	}
}

// failureText is the reason a document did not reach Drive.
func failureText(d docsbot.Document) string {
	if d.BackupStatus == docsbot.BackupTooLarge {
		return fmt.Sprintf(textReasonTooLarge, docsbot.MaxDriveBytes>>20)
	}
	switch failReason(d.BackupReason) {
	case docsbot.BackupReasonQuota:
		return textReasonQuota
	case docsbot.BackupReasonRevoked:
		return textReasonRevoked
	default:
		return textReasonUnavailable
	}
}

// failReason normalizes a stored backup reason; unknown and empty read as unavailable.
func failReason(r docsbot.BackupReason) docsbot.BackupReason {
	switch r {
	case docsbot.BackupReasonQuota, docsbot.BackupReasonRevoked:
		return r
	}
	return docsbot.BackupReasonUnavailable
}

// statusText assembles the message with at most lineCap document lines per category.
func statusText(st statusTally, folderByCat map[docsbot.Category]string, lineCap int) string {
	parts := []string{statusHeader(st)}
	if st.quota {
		parts = append(parts, textStatusQuotaHint)
	}
	for _, cat := range docsbot.AllCategories {
		if lines := st.lines[cat]; len(lines) > 0 {
			parts = append(parts, categoryBlock(cat, lines, folderByCat[cat], lineCap))
		}
	}
	return strings.Join(parts, "\n\n")
}

// statusHeader is the progress line with a bar while uploads run, then the
// outcome: all uploaded, or how many of them.
func statusHeader(st statusTally) string {
	switch {
	case st.done < st.total || st.total == 0:
		return fmt.Sprintf(textStatusProgress, st.done, st.total) + "\n" + progressBar(st.done, st.total)
	case st.notDone > 0:
		return fmt.Sprintf(textStatusPartial, st.backedUp, st.total)
	default:
		return fmt.Sprintf(textStatusDone, st.backedUp, plural(st.backedUp, pluralDocumentOne, pluralDocumentFew, pluralDocumentMany))
	}
}

// progressBar draws done/total as filled and empty segments; it is full only when done == total.
func progressBar(done, total int) string {
	filled := 0
	if total > 0 {
		filled = done * statusBarSegments / total
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", statusBarSegments-filled)
}

// categoryBlock is «📁 Label · K» (the label links to the Drive folder when it
// is known) over the document lines, cut to lineCap with «и ещё N документов».
func categoryBlock(cat docsbot.Category, lines []string, folderID string, lineCap int) string {
	label := html.EscapeString(cat.Label())
	if folderID != "" {
		label = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(folderURL(folderID)), label)
	}
	shown := lines
	if len(shown) > lineCap {
		shown = shown[:lineCap]
	}
	out := append([]string{fmt.Sprintf(textStatusCategory, label, len(lines))}, shown...)
	if hidden := len(lines) - len(shown); hidden > 0 {
		out = append(out, fmt.Sprintf(textStatusMore, hidden, plural(hidden, pluralDocumentOne, pluralDocumentFew, pluralDocumentMany)))
	}
	return strings.Join(out, "\n")
}

// statusKeyboard has «Повторить (R)» while some uploads failed and «Мои
// документы» once the batch is over; nil when neither applies.
func statusKeyboard(id docsbot.StatusMessageID, st statusTally) *models.InlineKeyboardMarkup {
	var row []models.InlineKeyboardButton
	if st.failed > 0 {
		row = append(row, models.InlineKeyboardButton{
			Text:         fmt.Sprintf("%s (%d)", btnRetryBackup, st.failed),
			CallbackData: docCB(actRetryBatch, "", string(id)),
		})
	}
	if st.done == st.total && st.total > 0 {
		row = append(row, models.InlineKeyboardButton{Text: btnMyDocs, CallbackData: docCB(actListBack, "")})
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
		{Text: btnRetryBackup, CallbackData: docCB(actRetryBatch, "", string(id))},
	}}}
}
