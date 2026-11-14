package telegram

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// PDF operations of the menu (conv:q:<docID>:<op>).
const (
	opRotate = "rot"
	opKeep   = "keep"
	opDrop   = "drop"
	opMerge  = "merge"
)

// pdfTimeout bounds one PDF operation; a pathological file must not hold a worker.
const pdfTimeout = 20 * time.Second

// pdfSource is the PDF an operation works on: a stored document (docID set) or
// a file sent to /convert. Only the Telegram file id is kept; the bytes are
// downloaded when the operation runs.
type pdfSource struct {
	docID  docsbot.DocumentID
	title  string
	fileID string
}

// onPDFOp runs a PDF operation of the card menu on a stored document. The menu
// message is the progress note and then the session's dialogue message; on an
// error it shows the menu again.
func (c *converter) onPDFOp(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64, doc docsbot.Document, op string) {
	menu := pdfMenuKeyboard(doc.ID)
	c.runPDFOp(ctx, b, chatID, update.CallbackQuery.From.ID, callbackMessageID(update),
		pdfSource{docID: doc.ID, title: doc.Title, fileID: doc.TgFileID}, op,
		func(msgID int, text string) { c.show(ctx, b, chatID, msgID, text, menu) })
}

// onSessionPDFOp runs an operation of the PDF menu of /convert (a PDF the user
// sent). The session is claimed for the run and goes back if the user can retry.
func (c *converter) onSessionPDFOp(ctx context.Context, b *bot.Bot, update *models.Update, chatID, tgUser int64, op string) {
	sess, ok := c.sessions.claim(tgUser, callbackMessageID(update), stepPDFMenu)
	if !ok || len(sess.pdfs) == 0 {
		c.stale(ctx, b, update, chatID)
		return
	}
	c.runPDFOp(ctx, b, chatID, tgUser, sess.msgID,
		pdfSource{title: sess.sourceTitle, fileID: sess.pdfs[0].fileID}, op,
		func(msgID int, text string) { c.retry(ctx, b, chatID, tgUser, sess, msgID, text, pdfActionsKeyboard()) })
}

// runPDFOp runs a PDF operation on src. Rotating gives the result at once; the
// others open a session that collects the rest of the input (page range, more
// PDFs). The dialogue message msgID turns into the progress note; fail shows an
// error on it.
func (c *converter) runPDFOp(ctx context.Context, b *bot.Bot, chatID, tgUser int64, msgID int, src pdfSource, op string, fail func(msgID int, text string)) {
	switch op {
	case opRotate, opKeep, opDrop, opMerge:
	default:
		return
	}
	user, _ := userID(ctx)
	msgID = c.show(ctx, b, chatID, msgID, textConvertProgress, nil)
	data, err := c.download(ctx, b, src.fileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", src.docID)
		fail(msgID, textConvertDownloadFailed)
		return
	}

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	if op == opRotate {
		out, err := convert.RotatePDF(pctx, data, 90)
		if err != nil {
			c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", user, "doc_id", src.docID, "op", op)
			fail(msgID, pdfErrorText(err))
			return
		}
		c.log.InfoContext(ctx, "pdf converted", "user_id", user, "doc_id", src.docID, "op", op, "in_bytes", len(data), "out_bytes", len(out))
		name := resultFileName(src.title, nameFallbackDocument, nameTagEdited, "pdf")
		if !c.sendResult(ctx, b, chatID, tgUser, name, out, src.docID, suffixEdited) {
			fail(msgID, textSendFailed)
			return
		}
		c.finish(ctx, b, chatID, msgID)
		return
	}

	// keep, drop and merge need more input: check the file now, then wait for it.
	pages, err := convert.PageCount(pctx, data)
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", user, "doc_id", src.docID, "op", op)
		fail(msgID, pdfErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "pdf session", "user_id", user, "doc_id", src.docID, "op", op, "pages", pages, "in_bytes", len(data))
	unlock := c.dialogue.lock(tgUser)
	defer unlock()
	if prev, ok := c.sessions.live(tgUser); ok {
		c.sessions.end(tgUser)
		if prev.msgID != 0 {
			c.out(b).clearKeyboard(ctx, chatID, prev.msgID)
		}
		c.out(b).send(ctx, chatID, textPDFPrevCancelled)
	}
	c.sessions.start(tgUser)
	next, prompt, kb := stepAwaitRange, textPDFRangePrompt, cancelKeyboard()
	if op == opMerge {
		next, prompt, kb = stepMergePDF, textPDFMergePrompt, pdfKeyboard()
	}
	c.sessions.update(tgUser, func(s *session) {
		s.step, s.pdfOp = next, op
		s.sourceDoc, s.sourceTitle = src.docID, src.title
		s.pdfs, s.pdfBytes = []pdfPart{{fileID: src.fileID, size: len(data)}}, len(data)
	})
	c.sessions.setMsg(tgUser, c.show(ctx, b, chatID, msgID, prompt, kb))
}

// onPDFSource takes the PDF a user sends after «Действия с PDF» and offers the
// operations.
func (c *converter) onPDFSource(ctx context.Context, b *bot.Bot, update *models.Update) {
	tgUser, _ := messageUser(update)
	chatID := update.Message.Chat.ID
	file := update.Message.Document
	if file.FileSize > convert.MaxPDFBytes {
		c.out(b).send(ctx, chatID, pdfTooLargeText())
		return
	}
	data, err := c.download(ctx, b, file.FileID)
	if err != nil {
		uid, _ := userID(ctx)
		c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid)
		c.out(b).send(ctx, chatID, textConvertDownloadFailed)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	if _, err := convert.PageCount(pctx, data); err != nil {
		c.out(b).send(ctx, chatID, pdfErrorText(err))
		return
	}

	unlock := c.dialogue.lock(tgUser)
	defer unlock()
	if _, ok := c.sessions.advance(tgUser, 0, []step{stepAwaitPDF}, func(s *session) {
		s.pdfs, s.pdfBytes = []pdfPart{{fileID: file.FileID, size: len(data)}}, len(data)
		s.sourceDoc, s.sourceTitle = "", file.FileName
		s.step = stepPDFMenu
	}); !ok {
		return
	}
	c.moveDown(ctx, b, chatID, tgUser, textConvertPDFMenu, pdfActionsKeyboard())
}

// onRange applies the typed page range to the session's PDF. The session is
// claimed first, so two messages can't run the operation twice; it goes back
// when the user can try again.
func (c *converter) onRange(ctx context.Context, b *bot.Bot, update *models.Update) {
	tgUser, _ := messageUser(update)
	chatID := update.Message.Chat.ID
	sess, ok := c.sessions.claim(tgUser, 0, stepAwaitRange)
	if !ok || len(sess.pdfs) == 0 {
		return
	}
	uid, _ := userID(ctx)
	msgID := c.show(ctx, b, chatID, sess.msgID, textConvertProgress, nil)
	// again puts the session back and asks once more at the bottom of the chat.
	again := func(text string) {
		unlock := c.dialogue.lock(tgUser)
		defer unlock()
		sess.msgID = msgID
		c.sessions.restore(tgUser, sess)
		c.moveDown(ctx, b, chatID, tgUser, text, cancelKeyboard())
	}
	src, err := c.download(ctx, b, sess.pdfs[0].fileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc)
		again(textConvertDownloadFailed)
		return
	}

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	out, err := convert.SelectPages(pctx, src, update.Message.Text, sess.pdfOp == opKeep)
	if errors.Is(err, convert.ErrBadPageRange) {
		again(textPDFBadRange)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc, "op", sess.pdfOp)
		c.show(ctx, b, chatID, msgID, pdfErrorText(err), nil)
		return
	}
	c.log.InfoContext(ctx, "pdf converted", "user_id", uid, "doc_id", sess.sourceDoc, "op", sess.pdfOp, "in_bytes", len(src), "out_bytes", len(out))
	name := resultFileName(sess.sourceTitle, nameFallbackDocument, nameTagEdited, "pdf")
	if !c.sendResult(ctx, b, chatID, tgUser, name, out, sess.sourceDoc, suffixEdited) {
		again(textSendFailed)
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

// onPDFPart adds a PDF the user sent to a merge in progress.
func (c *converter) onPDFPart(ctx context.Context, b *bot.Bot, update *models.Update) {
	tgUser, _ := messageUser(update)
	chatID := update.Message.Chat.ID
	sess, ok := c.sessions.live(tgUser)
	if !ok {
		return
	}
	if sess.step == stepAwaitPDF {
		c.onPDFSource(ctx, b, update)
		return
	}
	if sess.step == stepChooseAction || sess.step == stepPDFMenu {
		c.out(b).send(ctx, chatID, textConvertTapButton)
		return
	}
	if sess.step != stepMergePDF {
		c.out(b).send(ctx, chatID, textConvertUnsupported)
		return
	}
	file := update.Message.Document
	if int64(sess.pdfBytes)+file.FileSize > convert.MaxPDFBytes {
		c.out(b).send(ctx, chatID, pdfTooLargeText())
		return
	}
	// Download once to check the file; only its id and size are kept.
	data, err := c.download(ctx, b, file.FileID)
	if err != nil {
		uid, _ := userID(ctx)
		c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid)
		c.out(b).send(ctx, chatID, textConvertDownloadFailed)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	if _, err := convert.PageCount(pctx, data); err != nil {
		c.out(b).send(ctx, chatID, pdfErrorText(err))
		return
	}

	unlock := c.dialogue.lock(tgUser)
	defer unlock()
	total, tooLarge := 0, false
	c.sessions.update(tgUser, func(s *session) {
		if s.step != stepMergePDF {
			return
		}
		if s.pdfBytes+len(data) > convert.MaxPDFBytes {
			tooLarge = true
			return
		}
		s.pdfs = append(s.pdfs, pdfPart{fileID: file.FileID, size: len(data)})
		s.pdfBytes += len(data)
		total = len(s.pdfs)
	})
	if tooLarge {
		c.out(b).send(ctx, chatID, pdfTooLargeText())
		return
	}
	if total == 0 {
		return
	}
	c.moveDown(ctx, b, chatID, tgUser, fmt.Sprintf(textPDFMergeAdded, total), pdfKeyboard())
}

// finishMerge joins the collected PDFs. The session is claimed first, so a
// double «Готово» runs the merge once.
func (c *converter) finishMerge(ctx context.Context, b *bot.Bot, update *models.Update, tgUser, chatID int64) {
	unlock := c.dialogue.lock(tgUser) // see finishPDF
	msgID := callbackMessageID(update)
	sess, ok := c.sessions.claim(tgUser, msgID, stepMergePDF)
	if !ok {
		unlock()
		c.stale(ctx, b, update, chatID)
		return
	}
	uid, _ := userID(ctx)
	if len(sess.pdfs) < 2 {
		unlock()
		c.retry(ctx, b, chatID, tgUser, sess, msgID, textPDFMergeNeeded, pdfKeyboard())
		return
	}
	msgID = c.show(ctx, b, chatID, msgID, textConvertProgress, nil)
	unlock()

	files := make([][]byte, 0, len(sess.pdfs))
	total := 0
	for _, part := range sess.pdfs {
		data, err := c.download(ctx, b, part.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc)
			c.retry(ctx, b, chatID, tgUser, sess, msgID, textConvertDownloadFailed, pdfKeyboard())
			return
		}
		if total += len(data); total > convert.MaxPDFBytes {
			c.show(ctx, b, chatID, msgID, pdfTooLargeText(), nil)
			return
		}
		files = append(files, data)
	}

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	out, err := convert.MergePDF(pctx, files)
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc, "op", opMerge, "files", len(files))
		c.show(ctx, b, chatID, msgID, pdfErrorText(err), nil)
		return
	}
	c.log.InfoContext(ctx, "pdf converted", "user_id", uid, "doc_id", sess.sourceDoc, "op", opMerge, "files", len(files), "out_bytes", len(out))
	name := resultFileName(sess.sourceTitle, nameFallbackDocument, nameTagEdited, "pdf")
	if !c.sendResult(ctx, b, chatID, tgUser, name, out, sess.sourceDoc, suffixEdited) {
		c.retry(ctx, b, chatID, tgUser, sess, msgID, textSendFailed, pdfKeyboard())
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

func pdfTooLargeText() string { return fmt.Sprintf(textPDFTooLarge, convert.MaxPDFBytes>>20) }

// pdfErrorText maps a PDF error to a user message.
func pdfErrorText(err error) string {
	switch {
	case errors.Is(err, convert.ErrPDFTooLarge):
		return pdfTooLargeText()
	case errors.Is(err, convert.ErrPDFTooManyPages):
		return fmt.Sprintf(textPDFTooMany, convert.MaxPDFPages)
	case errors.Is(err, convert.ErrPDFTimeout):
		return textPDFTimeout
	case errors.Is(err, convert.ErrBadPageRange):
		return textPDFBadRange
	default:
		return textPDFInvalid
	}
}

// pdfMenuKeyboard is the menu of a PDF document.
func pdfMenuKeyboard(id docsbot.DocumentID) *models.InlineKeyboardMarkup {
	q := func(op string) string { return convDocCB(convActQuick, id, op) }
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnPDFRotate, CallbackData: q(opRotate)},
			{Text: btnPDFKeep, CallbackData: q(opKeep)},
			{Text: btnPDFDrop, CallbackData: q(opDrop)},
		},
		{
			{Text: btnPDFMerge, CallbackData: q(opMerge)},
			{Text: btnQuickCancel, CallbackData: cbMenuCancel},
		},
	}}
}

// pdfActionsKeyboard is the PDF menu of /convert: the operations are bound to
// the session, not to a stored document.
func pdfActionsKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: btnPDFRotate, CallbackData: cbSessRotate},
			{Text: btnPDFKeep, CallbackData: cbSessKeep},
		},
		{
			{Text: btnPDFDrop, CallbackData: cbSessDrop},
			{Text: btnPDFMerge, CallbackData: cbSessMerge},
		},
		{{Text: btnCancel, CallbackData: cbCancel}},
	}}
}
