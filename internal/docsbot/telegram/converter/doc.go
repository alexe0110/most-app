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
		c.out(b).Send(ctx, chatID, textError, tg.WithKeyboard(c.reportable(textError, nil, scenarioDoc)))
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

	if known && compressUseless(len(src), maxBytes, 0) {
		c.show(ctx, b, chatID, msgID, compressNothingText(maxBytes), menu)
		return
	}
	res, err := quickConvert(ctx, doc, src, op, maxBytes)
	if err != nil {
		c.log.ErrorContext(ctx, "convert document", "err", err, "user_id", user, "doc_id", id, "op", op)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), menu)
		return
	}
	if known && compressUseless(len(src), maxBytes, len(res.data)) {
		c.log.InfoContext(ctx, "compression did not help", "user_id", user, "doc_id", id, "in_bytes", len(src), "out_bytes", len(res.data))
		c.show(ctx, b, chatID, msgID, compressNothingText(maxBytes), menu)
		return
	}
	c.log.InfoContext(ctx, "document converted", "user_id", user, "doc_id", id, "op", op,
		"in_bytes", len(src), "out_bytes", len(res.data))
	if known && len(res.data) > maxBytes {
		c.out(b).Send(ctx, chatID, fmt.Sprintf(textConvertBestEffort, len(res.data)/1_024))
	}
	res.sourceDoc = id
	if !c.sendResult(ctx, b, chatID, p.tgUser, res) {
		c.show(ctx, b, chatID, msgID, textSendFailed, c.reportable(textSendFailed, menu, scenarioDoc))
		return
	}
	c.done(ctx, quickConvertOp(op))
	c.finish(ctx, b, chatID, msgID)
}

// quickConvert runs a quick operation of the menu on the downloaded source.
func quickConvert(ctx context.Context, doc docsbot.Document, src []byte, op string, maxBytes int) (resultFile, error) {
	ictx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	switch op {
	case opPDF:
		out, err := convert.ImagesToPDF(ictx, [][]byte{src})
		return resultFile{name: resultFileName(doc.Title, nameFallbackPhoto, nameTagPDF, "pdf"), data: out, kind: resultPDF}, err
	case opJPG:
		out, err := convert.ToJPEG(ictx, src)
		return resultFile{name: resultFileName(doc.Title, nameFallbackPhoto, nameTagJPG, "jpg"), data: out, kind: resultJPG}, err
	default:
		out, err := convert.Compress(ictx, src, maxBytes, convert.JPEG)
		return resultFile{
			name: resultFileName(doc.Title, nameFallbackPhoto, nameTagCompressed, "jpg"), data: out, kind: resultCompressed,
			caption: compressedCaption(len(src), len(out)),
		}, err
	}
}

// onSave handles «Сохранить»: conv:sv:<token>. A result without a source document
// becomes a new document and gets the usual category question. One made from a
// document that still waits for its category takes that document's place, so the
// category is chosen in the message above, as usual.
func (c *Converter) onSave(ctx context.Context, b *bot.Bot, p press) {
	token := p.data.token()
	res, ok := c.results.peek(token, p.tgUser)
	if !ok {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	if res.sourceDoc == "" {
		c.saveNew(ctx, b, p, token)
		return
	}

	card, err := c.svc.Card(ctx, res.user, res.sourceDoc)
	switch {
	case errors.Is(err, docsbot.ErrNotFound):
		c.results.take(token, p.tgUser) // nothing to put the result in
		c.clearButtons(ctx, b, p)
		c.out(b).Send(ctx, p.chatID, textGone)
		return
	case err != nil:
		c.log.ErrorContext(ctx, "load result source", "err", err, "user_id", res.user, "doc_id", res.sourceDoc)
		c.out(b).Send(ctx, p.chatID, textError, tg.WithKeyboard(c.reportable(textError, nil, scenarioDoc)))
		return
	case card.Doc.Status == docsbot.StatusSaved:
		// Saved since the result was sent: replacing is the user's call.
		c.onReplaceAsk(ctx, b, p)
		return
	}
	taken, ok := c.results.take(token, p.tgUser) // a double tap swaps once
	if !ok {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.replaceSource(ctx, b, p, token, taken, true)
}

// saveNew stores a result that has no source document as a new pending one and
// asks for its category.
func (c *Converter) saveNew(ctx context.Context, b *bot.Bot, p press, token string) {
	res, ok := c.results.take(token, p.tgUser)
	if !ok {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, p)

	file := res.file
	file.UserID = res.user
	rep, err := c.svc.Receive(ctx, file)
	if err != nil {
		c.log.ErrorContext(ctx, "save converted file", "err", err, "user_id", res.user)
		c.out(b).Send(ctx, p.chatID, textError, tg.WithKeyboard(c.reportable(textError, nil, scenarioDoc)))
		return
	}
	if r, ok := rep.(docsbot.AlreadySaved); ok {
		c.docs.SendAlreadySaved(ctx, b, p.chatID, res.user, r.Doc.ID)
		return
	}
	c.docs.SendNotice(ctx, b, p.chatID, rep)
}

// onLegacySave answers the «Сохранить» buttons of result messages sent before
// conv:sv existed: they carry conv:save, whose result is no longer stored.
func (c *Converter) onLegacySave(ctx context.Context, b *bot.Bot, p press) {
	c.out(b).Send(ctx, p.chatID, textConvertResultOld)
	c.clearButtons(ctx, b, p)
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
	res, ok := c.results.peek(token, p.tgUser)
	if !ok {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.editResultCaption(ctx, b, p, "", resultKeyboard(token, res.kind, true))
}

// onReplace swaps the source document's file for the result: conv:rpy:<token>.
// The result is taken first, so a double tap replaces once.
func (c *Converter) onReplace(ctx context.Context, b *bot.Bot, p press) {
	token := p.data.token()
	res, ok := c.results.take(token, p.tgUser)
	if !ok || res.sourceDoc == "" {
		c.out(b).Send(ctx, p.chatID, textConvertResultOld)
		return
	}
	c.replaceSource(ctx, b, p, token, res, false)
}

// replaceSource swaps the file of the result's source document for the result,
// which the caller has already taken, and shows the outcome. When the document
// cannot be replaced right now the result is put back, so the user can try again.
// pending is set for a source that still waits for its category: the message then
// still has its own caption (no confirmation was shown) and the outcome is a
// short note instead of a card.
func (c *Converter) replaceSource(ctx context.Context, b *bot.Bot, p press, token string, res convResult, pending bool) {
	chatID := p.chatID
	end := func() {
		if pending {
			c.clearButtons(ctx, b, p)
			return
		}
		c.editResultCaption(ctx, b, p, "", nil) // the confirmation goes with the buttons
	}
	retry := func(text string) {
		c.results.put(token, p.tgUser, res)
		if !pending {
			c.editResultCaption(ctx, b, p, "", resultKeyboard(token, res.kind, true))
		}
		c.out(b).Send(ctx, chatID, text, tg.WithKeyboard(c.reportable(text, nil, scenarioDoc)))
	}

	rep, err := c.svc.ReplaceContent(ctx, res.user, res.sourceDoc, res.file)
	if errors.Is(err, docsbot.ErrNotFound) {
		end()
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
		end()
		if pending {
			c.out(b).Send(ctx, chatID, textResultSwapped)
			return
		}
		c.docs.SendReplaced(ctx, b, chatID, res.user, r.Doc.ID)
	case docsbot.AlreadySaved:
		end()
		c.docs.SendAlreadySaved(ctx, b, chatID, res.user, r.Doc.ID)
	default:
		end()
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
