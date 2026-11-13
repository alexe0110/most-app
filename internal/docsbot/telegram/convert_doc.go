package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// loadDoc reads the user's document (the domain checks ownership) and answers
// the user itself when it can't.
func (c *converter) loadDoc(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, bool) {
	card, err := c.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.out(b).send(ctx, chatID, textGone)
		return docsbot.Document{}, false
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert load document", "err", err, "user_id", user, "doc_id", id)
		c.out(b).send(ctx, chatID, textError)
		return docsbot.Document{}, false
	}
	return card.Doc, true
}

// onMenu offers the operations that fit the document's type.
func (c *converter) onMenu(ctx context.Context, b *bot.Bot, chatID int64, id docsbot.DocumentID) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	switch {
	case isPDF(doc):
		c.out(b).send(ctx, chatID, textConvertMenu, withKeyboard(pdfMenuKeyboard(id)))
	case convertible(doc):
		c.out(b).send(ctx, chatID, textConvertMenu, withKeyboard(quickKeyboard(id, isWebP(doc))))
	default:
		c.out(b).send(ctx, chatID, unsupportedText(doc))
	}
}

// onQuick converts a stored document straight from the menu: conv:q:<docID>:<op>.
func (c *converter) onQuick(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	rest := strings.TrimPrefix(update.CallbackQuery.Data, cbQuickPrefix)
	rawID, op, ok := strings.Cut(rest, ":")
	if !ok || rawID == "" {
		return
	}
	user, ok := userID(ctx)
	if !ok {
		return
	}
	release, ok := c.claimMenu(chatID, callbackMessageID(update))
	if !ok {
		// The running tap owns the message: only tell the user, keep its buttons.
		c.out(b).send(ctx, chatID, textStale)
		return
	}
	defer release()
	id := docsbot.DocumentID(rawID)

	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	if !convertible(doc) {
		c.out(b).send(ctx, chatID, unsupportedText(doc))
		return
	}
	if isPDF(doc) {
		c.onPDFOp(ctx, b, update, chatID, doc, op)
		return
	}
	maxBytes, known := quickLimit(op)
	if !known && op != opPDF && (op != opJPG || !isWebP(doc)) {
		return
	}
	menu := quickKeyboard(id, isWebP(doc))
	msgID := c.show(ctx, b, chatID, callbackMessageID(update), textConvertProgress, nil)
	src, err := c.download(ctx, b, doc.TgFileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", id)
		c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, menu)
		return
	}

	var out []byte
	var name, suffix string
	switch op {
	case opPDF:
		suffix = suffixPDF
		out, err = convert.ImagesToPDF([][]byte{src})
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagPDF, "pdf")
	case opJPG:
		suffix = suffixJPG
		out, err = convert.ToJPEG(src)
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagJPG, "jpg")
	default:
		suffix = suffixCompressed
		out, err = convert.Compress(src, maxBytes, convert.JPEG)
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagCompressed, "jpg")
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert document", "err", err, "user_id", user, "doc_id", id, "op", op)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), menu)
		return
	}
	c.log.InfoContext(ctx, "document converted", "user_id", user, "doc_id", id, "op", op,
		"in_bytes", len(src), "out_bytes", len(out))
	if known && len(out) > maxBytes {
		c.out(b).send(ctx, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	if !c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, out, id, suffix) {
		c.show(ctx, b, chatID, msgID, textSendFailed, menu)
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

// onSave stores the result as a document: conv:save:<token>.
func (c *converter) onSave(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbSavePrefix)
	res, ok := c.results.take(token, update.CallbackQuery.From.ID)
	if !ok {
		c.out(b).send(ctx, chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, update, chatID)

	rep, err := c.svc.SaveDerived(ctx, res.user, res.sourceDoc, res.file, res.suffix)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.out(b).send(ctx, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "save converted file", "err", err, "user_id", res.user)
		c.out(b).send(ctx, chatID, textError)
		return
	}

	switch r := rep.(type) {
	case docsbot.Saved:
		// The result message is a file, which can't be edited into a card: the
		// card replaces a fresh placeholder instead.
		id := c.out(b).send(ctx, chatID, textConvertSaving)
		if id == 0 {
			return
		}
		c.renderSaved(ctx, b, chatID, id, res.user, r)
	case docsbot.NeedCategory:
		c.out(b).send(ctx, chatID, textChooseCategory, withKeyboard(categoryKeyboard(actCategory, r.Doc.ID)))
	case docsbot.AlreadySaved:
		c.sendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		if text, ok := rejectionText(rep); ok {
			c.out(b).send(ctx, chatID, text)
		}
	}
}

// onReplaceAsk turns the result's caption into a confirmation: conv:rp:<token>.
// The result stays in the store until the user confirms or cancels.
func (c *converter) onReplaceAsk(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepAsk)
	res, ok := c.results.peek(token, update.CallbackQuery.From.ID)
	if !ok || res.sourceDoc == "" {
		c.out(b).send(ctx, chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, update, chatID, textReplaceConfirm, replaceKeyboard(token))
}

// onReplaceCancel puts the result's caption and buttons back: conv:rpn:<token>.
func (c *converter) onReplaceCancel(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepNo)
	if _, ok := c.results.peek(token, update.CallbackQuery.From.ID); !ok {
		c.out(b).send(ctx, chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, update, chatID, "", saveKeyboard(token, true))
}

// onReplace swaps the source document's file for the result: conv:rpy:<token>.
// The result is taken first, so a double tap replaces once. When the document
// cannot be replaced right now the result is put back, so the user can try again.
func (c *converter) onReplace(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbRepYes)
	res, ok := c.results.take(token, update.CallbackQuery.From.ID)
	if !ok || res.sourceDoc == "" {
		c.out(b).send(ctx, chatID, textConvertResultOld)
		return
	}
	retry := func(text string) {
		c.results.put(token, res)
		c.editResultCaption(ctx, b, update, chatID, "", saveKeyboard(token, true))
		c.out(b).send(ctx, chatID, text)
	}

	rep, err := c.svc.ReplaceContent(ctx, res.user, res.sourceDoc, res.file)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		c.out(b).send(ctx, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "replace original", "err", err, "user_id", res.user, "doc_id", res.sourceDoc)
		retry(textError)
		return
	}

	switch r := rep.(type) {
	case docsbot.ReplaceBusy:
		retry(textReplaceBusy)
	case docsbot.ReplaceMissing:
		retry(textReplaceMissing)
	case docsbot.Replaced:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		c.sendReplaced(ctx, b, chatID, res.user, r.Doc.ID)
	case docsbot.AlreadySaved:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		c.sendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		c.editResultCaption(ctx, b, update, chatID, "", nil)
		if text, ok := rejectionText(rep); ok {
			c.out(b).send(ctx, chatID, text)
		}
	}
}

// editResultCaption sets the caption and buttons (nil removes them) of the
// result message a button was pressed on, best effort.
func (c *converter) editResultCaption(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64, caption string, kb *models.InlineKeyboardMarkup) {
	m := update.CallbackQuery.Message.Message
	if m == nil {
		return
	}
	c.out(b).edit(ctx, chatID, m.ID, caption, onCaption(), withKeyboard(kb))
}
