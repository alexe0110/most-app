package converter

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// loadDoc reads the user's document (the domain checks ownership) and answers
// the user itself when it can't.
func (c *Converter) loadDoc(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, bool) {
	card, err := c.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.out(b).Send(ctx, chatID, textGone)
		return docsbot.Document{}, false
	}
	if err != nil {
		c.log.ErrorContext(ctx, "convert load document", "err", err, "user_id", user, "doc_id", id)
		c.out(b).Send(ctx, chatID, textError)
		return docsbot.Document{}, false
	}
	return card.Doc, true
}

// onMenu offers the operations that fit the document's type.
func (c *Converter) onMenu(ctx context.Context, b *bot.Bot, chatID int64, id docsbot.DocumentID) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	switch {
	case IsPDF(doc):
		c.out(b).Send(ctx, chatID, textConvertMenu, tg.WithKeyboard(pdfMenuKeyboard(id)))
	case Convertible(doc):
		c.out(b).Send(ctx, chatID, textConvertMenu, tg.WithKeyboard(quickKeyboard(id, isWebP(doc))))
	default:
		c.out(b).Send(ctx, chatID, unsupportedText(doc))
	}
}

// onQuick converts a stored document straight from the menu: conv:q:<docID>:<op>.
func (c *Converter) onQuick(ctx context.Context, b *bot.Bot, p press) {
	chatID, id, op := p.chatID, p.data.docID, p.data.token()
	if id == "" || len(p.data.args) == 0 || !p.hasUser {
		return
	}
	user := p.user
	release, ok := c.claimMenu(chatID, p.msgID)
	if !ok {
		// The running tap owns the message: only tell the user, keep its buttons.
		c.out(b).Send(ctx, chatID, textStale)
		return
	}
	defer release()

	doc, ok := c.loadDoc(ctx, b, chatID, user, id)
	if !ok {
		return
	}
	if !Convertible(doc) {
		c.out(b).Send(ctx, chatID, unsupportedText(doc))
		return
	}
	if IsPDF(doc) {
		c.onPDFOp(ctx, b, p, doc, op)
		return
	}
	maxBytes, known := quickLimit(op)
	if !known && op != opPDF && (op != opJPG || !isWebP(doc)) {
		return
	}
	menu := quickKeyboard(id, isWebP(doc))
	msgID := c.show(ctx, b, chatID, p.msgID, textConvertProgress, nil)
	src, err := c.download(ctx, doc.TgFileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download document", "err", err, "user_id", user, "doc_id", id)
		c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, menu)
		return
	}

	var out []byte
	var name, suffix, caption string
	ictx, cancelImage := context.WithTimeout(ctx, imageTimeout)
	switch op {
	case opPDF:
		suffix = suffixPDF
		out, err = convert.ImagesToPDF(ictx, [][]byte{src})
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagPDF, "pdf")
	case opJPG:
		suffix = suffixJPG
		out, err = convert.ToJPEG(ictx, src)
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagJPG, "jpg")
	default:
		suffix = suffixCompressed
		out, err = convert.Compress(ictx, src, maxBytes, convert.JPEG)
		caption = compressedCaption(len(src), len(out))
		name = resultFileName(doc.Title, nameFallbackPhoto, nameTagCompressed, "jpg")
	}
	cancelImage()
	if err != nil {
		c.log.ErrorContext(ctx, "convert document", "err", err, "user_id", user, "doc_id", id, "op", op)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), menu)
		return
	}
	c.log.InfoContext(ctx, "document converted", "user_id", user, "doc_id", id, "op", op,
		"in_bytes", len(src), "out_bytes", len(out))
	if known && len(out) > maxBytes {
		c.out(b).Send(ctx, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	if !c.sendResult(ctx, b, chatID, p.tgUser, resultFile{name: name, data: out, sourceDoc: id, suffix: suffix, caption: caption}) {
		c.show(ctx, b, chatID, msgID, textSendFailed, menu)
		return
	}
	c.done(ctx, quickConvertOp(op))
	c.finish(ctx, b, chatID, msgID)
}

// onSave stores the result as a document: conv:save:<token>.
func (c *Converter) onSave(ctx context.Context, b *bot.Bot, p press) {
	chatID := p.chatID
	res, ok := c.results.take(p.data.token(), p.tgUser)
	if !ok {
		c.out(b).Send(ctx, chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, p)

	rep, err := c.svc.SaveDerived(ctx, res.user, res.sourceDoc, res.file, res.suffix)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.out(b).Send(ctx, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "save converted file", "err", err, "user_id", res.user)
		c.out(b).Send(ctx, chatID, textError)
		return
	}

	switch r := rep.(type) {
	case docsbot.Saved:
		// The result message is a file, which can't be edited into a card: the
		// card replaces a fresh placeholder instead.
		id := c.out(b).Send(ctx, chatID, textConvertSaving)
		if id == 0 {
			return
		}
		c.docs.RenderSaved(ctx, b, chatID, id, res.user, r)
	case docsbot.AlreadySaved:
		c.docs.SendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		c.docs.SendNotice(ctx, b, chatID, rep)
	}
}

// onReplaceAsk turns the result's caption into a confirmation: conv:rp:<token>.
// The result stays in the store until the user confirms or cancels.
func (c *Converter) onReplaceAsk(ctx context.Context, b *bot.Bot, p press) {
	token := p.data.token()
	res, ok := c.results.peek(token, p.tgUser)
	if !ok || res.sourceDoc == "" {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, p, textReplaceConfirm, replaceKeyboard(token))
}

// onReplaceCancel puts the result's caption and buttons back: conv:rpn:<token>.
func (c *Converter) onReplaceCancel(ctx context.Context, b *bot.Bot, p press) {
	token := p.data.token()
	if _, ok := c.results.peek(token, p.tgUser); !ok {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, p, "", saveKeyboard(token, true))
}

// onReplace swaps the source document's file for the result: conv:rpy:<token>.
// The result is taken first, so a double tap replaces once. When the document
// cannot be replaced right now the result is put back, so the user can try again.
func (c *Converter) onReplace(ctx context.Context, b *bot.Bot, p press) {
	chatID, token := p.chatID, p.data.token()
	res, ok := c.results.take(token, p.tgUser)
	if !ok || res.sourceDoc == "" {
		c.out(b).Send(ctx, chatID, textConvertResultOld)
		return
	}
	retry := func(text string) {
		c.results.put(token, p.tgUser, res)
		c.editResultCaption(ctx, b, p, "", saveKeyboard(token, true))
		c.out(b).Send(ctx, chatID, text)
	}

	rep, err := c.svc.ReplaceContent(ctx, res.user, res.sourceDoc, res.file)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.editResultCaption(ctx, b, p, "", nil)
		c.out(b).Send(ctx, chatID, textGone)
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
		c.editResultCaption(ctx, b, p, "", nil)
		c.docs.SendReplaced(ctx, b, chatID, res.user, r.Doc.ID)
	case docsbot.AlreadySaved:
		c.editResultCaption(ctx, b, p, "", nil)
		c.docs.SendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		c.editResultCaption(ctx, b, p, "", nil)
		c.docs.SendNotice(ctx, b, chatID, rep)
	}
}

// editResultCaption sets the caption and buttons (nil removes them) of the
// result message a button was pressed on, best effort.
func (c *Converter) editResultCaption(ctx context.Context, b *bot.Bot, p press, caption string, kb *models.InlineKeyboardMarkup) {
	if !p.hasMsg {
		return
	}
	c.out(b).Edit(ctx, p.chatID, p.msgID, caption, tg.OnCaption(), tg.WithKeyboard(kb))
}
