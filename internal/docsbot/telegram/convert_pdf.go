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

// onPDFOp runs a PDF operation of the menu on a stored document. Rotating gives
// the result at once; the others open a session that collects the rest of the
// input (page range, more PDFs).
func (c *converter) onPDFOp(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64, user docsbot.UserID, doc docsbot.Document, op string) {
	switch op {
	case opRotate, opKeep, opDrop, opMerge:
	default:
		return
	}
	src, err := c.download(ctx, b, doc.TgFileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", doc.ID)
		reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
		return
	}
	tgUser := update.CallbackQuery.From.ID

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	if op == opRotate {
		out, err := convert.RotatePDF(pctx, src, 90)
		if err != nil {
			c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", user, "doc_id", doc.ID, "op", op)
			reply(ctx, b, c.log, chatID, pdfErrorText(err))
			return
		}
		c.log.InfoContext(ctx, "pdf converted", "user_id", user, "doc_id", doc.ID, "op", op, "in_bytes", len(src), "out_bytes", len(out))
		c.sendResult(ctx, b, chatID, tgUser, resultName(doc.Title, suffixEdited, "document", "pdf"), out, doc.ID, suffixEdited)
		return
	}

	// keep, drop and merge need more input: check the file now, then wait for it.
	pages, err := convert.PageCount(pctx, src)
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", user, "doc_id", doc.ID, "op", op)
		reply(ctx, b, c.log, chatID, pdfErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "pdf session", "user_id", user, "doc_id", doc.ID, "op", op, "pages", pages, "in_bytes", len(src))
	if c.sessions.end(tgUser) {
		reply(ctx, b, c.log, chatID, textPDFPrevCancelled)
	}
	c.sessions.start(tgUser)
	next, prompt, kb := stepAwaitRange, textPDFRangePrompt, cancelKeyboard()
	if op == opMerge {
		next, prompt, kb = stepMergePDF, textPDFMergePrompt, pdfKeyboard()
	}
	c.sessions.update(tgUser, func(s *session) {
		s.step, s.pdfOp = next, op
		s.sourceDoc, s.sourceTitle = doc.ID, doc.Title
		s.pdfs, s.pdfBytes = []pdfPart{{fileID: doc.TgFileID, size: len(src)}}, len(src)
	})
	sendMarkup(ctx, b, c.log, chatID, prompt, kb)
}

// onRange applies the typed page range to the session's PDF. The session is
// claimed first, so two messages can't run the operation twice; it goes back
// when the user can try again.
func (c *converter) onRange(ctx context.Context, b *bot.Bot, update *models.Update) {
	tgUser, _ := messageUser(update)
	chatID := update.Message.Chat.ID
	sess, ok := c.sessions.claim(tgUser, stepAwaitRange)
	if !ok || len(sess.pdfs) == 0 {
		return
	}
	uid, _ := userID(ctx)
	src, err := c.download(ctx, b, sess.pdfs[0].fileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc)
		c.sessions.restore(tgUser, sess)
		reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
		return
	}

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	out, err := convert.SelectPages(pctx, src, update.Message.Text, sess.pdfOp == opKeep)
	if errors.Is(err, convert.ErrBadPageRange) {
		c.sessions.restore(tgUser, sess) // try again
		reply(ctx, b, c.log, chatID, textPDFBadRange)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc, "op", sess.pdfOp)
		reply(ctx, b, c.log, chatID, pdfErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "pdf converted", "user_id", uid, "doc_id", sess.sourceDoc, "op", sess.pdfOp, "in_bytes", len(src), "out_bytes", len(out))
	c.sendResult(ctx, b, chatID, tgUser, resultName(sess.sourceTitle, suffixEdited, "document", "pdf"), out, sess.sourceDoc, suffixEdited)
}

// onPDFPart adds a PDF the user sent to a merge in progress.
func (c *converter) onPDFPart(ctx context.Context, b *bot.Bot, update *models.Update) {
	tgUser, _ := messageUser(update)
	chatID := update.Message.Chat.ID
	sess, ok := c.sessions.live(tgUser)
	if !ok {
		return
	}
	if sess.step != stepMergePDF {
		reply(ctx, b, c.log, chatID, textConvertUnsupported)
		return
	}
	file := update.Message.Document
	if int64(sess.pdfBytes)+file.FileSize > convert.MaxPDFBytes {
		reply(ctx, b, c.log, chatID, pdfTooLargeText())
		return
	}
	// Download once to check the file; only its id and size are kept.
	data, err := c.download(ctx, b, file.FileID)
	if err != nil {
		uid, _ := userID(ctx)
		c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid)
		reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
		return
	}
	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	if _, err := convert.PageCount(pctx, data); err != nil {
		reply(ctx, b, c.log, chatID, pdfErrorText(err))
		return
	}

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
		reply(ctx, b, c.log, chatID, pdfTooLargeText())
		return
	}
	reply(ctx, b, c.log, chatID, fmt.Sprintf(textPDFMergeAdded, total))
}

// finishMerge joins the collected PDFs. The session is claimed first, so a
// double «Готово» runs the merge once.
func (c *converter) finishMerge(ctx context.Context, b *bot.Bot, tgUser, chatID int64) {
	sess, ok := c.sessions.claim(tgUser, stepMergePDF)
	if !ok {
		return
	}
	uid, _ := userID(ctx)
	if len(sess.pdfs) < 2 {
		c.sessions.restore(tgUser, sess)
		reply(ctx, b, c.log, chatID, textPDFMergeNeeded)
		return
	}
	files := make([][]byte, 0, len(sess.pdfs))
	total := 0
	for _, part := range sess.pdfs {
		data, err := c.download(ctx, b, part.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc)
			c.sessions.restore(tgUser, sess)
			reply(ctx, b, c.log, chatID, textConvertDownloadFailed)
			return
		}
		if total += len(data); total > convert.MaxPDFBytes {
			reply(ctx, b, c.log, chatID, pdfTooLargeText())
			return
		}
		files = append(files, data)
	}

	pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()
	out, err := convert.MergePDF(pctx, files)
	if err != nil {
		c.log.ErrorContext(ctx, "convert pdf", "err", err, "user_id", uid, "doc_id", sess.sourceDoc, "op", opMerge, "files", len(files))
		reply(ctx, b, c.log, chatID, pdfErrorText(err))
		return
	}
	c.log.InfoContext(ctx, "pdf converted", "user_id", uid, "doc_id", sess.sourceDoc, "op", opMerge, "files", len(files), "out_bytes", len(out))
	c.sendResult(ctx, b, chatID, tgUser, resultName(sess.sourceTitle, suffixEdited, "document", "pdf"), out, sess.sourceDoc, suffixEdited)
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
	q := func(op string) string { return cbQuickPrefix + string(id) + ":" + op }
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
