package converter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// MIME types of the Office files the info screen reads.
const (
	mimeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	mimeXLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	mimePPTX = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

// errDownload marks a failure to fetch the file, as opposed to reading it.
var errDownload = errors.New("download file")

// cleanedCopy is a cleaned file waiting for the choice between it and the
// original. The copy is already in Telegram: file holds the ids Telegram returned.
type cleanedCopy struct {
	user      docsbot.UserID
	sourceDoc docsbot.DocumentID
	file      docsbot.IncomingFile
}

// Inspectable reports whether the «О файле» screen has something to say about
// the document: a photo (it was compressed by Telegram) or a file of a supported
// type within the download limit. The real type is found from the bytes later;
// this only decides whether to offer the button.
func Inspectable(doc docsbot.Document) bool {
	switch doc.Kind {
	case docsbot.KindPhoto:
		return true
	case docsbot.KindDocument:
		return (doc.FileSize == 0 || doc.FileSize <= tg.MaxFileBytes) && inspectableType(doc.MIME, doc.Title)
	}
	return false
}

// inspectableType judges by the declared MIME type or, when it is missing or
// generic, by the file extension.
func inspectableType(mime, name string) bool {
	switch mime {
	case "image/jpeg", "image/jpg", mimePNG, "image/webp", "image/heic", "image/heif", mimePDF, mimeDOCX, mimeXLSX, mimePPTX:
		return true
	case "", "application/octet-stream":
		switch strings.ToLower(filepath.Ext(name)) {
		case ".jpg", ".jpeg", ".png", ".webp", ".heic", ".heif", ".pdf", ".docx", ".xlsx", ".pptx":
			return true
		}
	}
	return false
}

// canClean reports whether cleaning would change anything and can be done.
func canClean(info convert.FileInfo) bool {
	return !info.Clean() && (info.PDF == nil || !info.PDF.Encrypted)
}

// onInfo shows what is hidden in a stored document: conv:i:<docID>. The message
// it was pressed on (the picker) stays as it is.
func (c *Converter) onInfo(ctx context.Context, b *bot.Bot, p press) {
	chatID, id := p.chatID, p.data.docID
	if id == "" || !p.hasUser {
		return
	}
	release, ok := c.claimMenu(chatID, p.msgID)
	if !ok {
		return // a double tap: the running one answers
	}
	defer release()

	doc, ok := c.loadDoc(ctx, b, chatID, p.user, id)
	if !ok {
		return
	}
	out, back := c.out(b), infoKeyboard(id, false)
	switch {
	case doc.Kind == docsbot.KindPhoto:
		out.Send(ctx, chatID, textInfoCompressedPhoto, tg.WithKeyboard(back))
		return
	case !Inspectable(doc):
		out.Send(ctx, chatID, textInfoUnsupported, tg.WithKeyboard(back))
		return
	}
	_, info, err := c.loadInfo(ctx, doc)
	if err != nil {
		c.logMetaFailure(ctx, "inspect file", err, "user_id", p.user, "doc_id", id)
		out.Send(ctx, chatID, infoErrorText(err), tg.WithKeyboard(back))
		return
	}
	c.log.InfoContext(ctx, "file inspected", "user_id", p.user, "doc_id", id, "format", info.Type,
		"bytes", info.Size, "clean", info.Clean())
	out.Send(ctx, chatID, renderInfo(doc.Title, info),
		tg.WithHTML(), tg.WithoutPreview(), tg.WithKeyboard(infoKeyboard(id, canClean(info))))
}

// loadInfo downloads the document and reads its metadata, under imageTimeout.
func (c *Converter) loadInfo(ctx context.Context, doc docsbot.Document) ([]byte, convert.FileInfo, error) {
	data, err := c.download(ctx, doc.TgFileID)
	if err != nil {
		return nil, convert.FileInfo{}, fmt.Errorf("%w: %w", errDownload, err)
	}
	ictx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	info, err := convert.Inspect(ictx, data)
	if err != nil {
		return nil, convert.FileInfo{}, err
	}
	return data, info, nil
}

// onClean sends a copy of the document without metadata: conv:cl:<docID>, pressed
// on the info screen, which becomes the progress message and is removed at the end.
func (c *Converter) onClean(ctx context.Context, b *bot.Bot, p press) {
	chatID, id := p.chatID, p.data.docID
	if id == "" || !p.hasUser {
		return
	}
	release, ok := c.claimMenu(chatID, p.msgID)
	if !ok {
		return
	}
	defer release()

	doc, ok := c.loadDoc(ctx, b, chatID, p.user, id)
	if !ok {
		return
	}
	if doc.Kind != docsbot.KindDocument || !Inspectable(doc) {
		c.out(b).Send(ctx, chatID, textInfoUnsupported)
		return
	}
	back := infoKeyboard(id, false)
	msgID := c.show(ctx, b, chatID, p.msgID, textConvertProgress, nil)

	data, before, err := c.loadInfo(ctx, doc)
	if err != nil {
		c.logMetaFailure(ctx, "inspect file", err, "user_id", p.user, "doc_id", id)
		c.show(ctx, b, chatID, msgID, infoErrorText(err), back)
		return
	}
	if !canClean(before) {
		c.show(ctx, b, chatID, msgID, textInfoNothingToClean, back)
		return
	}
	cleaned, err := c.strip(ctx, data)
	if err != nil {
		c.logMetaFailure(ctx, "clean file", err, "user_id", p.user, "doc_id", id, "format", before.Type)
		c.show(ctx, b, chatID, msgID, infoErrorText(err), infoKeyboard(id, true))
		return
	}

	name := resultFileName(doc.Title, nameFallbackDocument, nameTagClean, fileExt(before.Type))
	caption := fmt.Sprintf(textCleanedCaption, blockNames(before, true))
	if !c.sendCleaned(ctx, b, chatID, p.tgUser, name, caption, cleaned, id) {
		c.show(ctx, b, chatID, msgID, textSendFailed, infoKeyboard(id, true))
		return
	}
	c.log.InfoContext(ctx, "file cleaned", "user_id", p.user, "doc_id", id, "format", before.Type,
		"in_bytes", len(data), "out_bytes", len(cleaned))
	c.finish(ctx, b, chatID, msgID)
}

// strip removes the metadata and checks the result: a copy that still reads as
// dirty is never offered, so «чистый» is never a promise the file does not keep.
func (c *Converter) strip(ctx context.Context, data []byte) ([]byte, error) {
	sctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	cleaned, err := convert.Strip(sctx, data)
	if err != nil {
		return nil, err
	}
	after, err := convert.Inspect(sctx, cleaned)
	if err != nil {
		return nil, err
	}
	if !after.Clean() {
		return nil, errStillDirty
	}
	return cleaned, nil
}

// errStillDirty means the stripped file still carries metadata.
var errStillDirty = errors.New("metadata left after cleaning")

// sendCleaned uploads the cleaned copy with the keep-which buttons and reports
// whether Telegram accepted it. As in sendResult the choice is remembered only if
// Telegram echoes the stored file back.
func (c *Converter) sendCleaned(ctx context.Context, b *bot.Bot, chatID, tgUser int64, filename, caption string,
	data []byte, sourceDoc docsbot.DocumentID,
) bool {
	params := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
		Caption:  caption,
	}
	user, haveUser := userID(ctx)
	token := tg.NewToken()
	if haveUser {
		params.ReplyMarkup = cleanedKeyboard(token)
	}
	msg, err := b.SendDocument(ctx, params)
	if err != nil {
		c.log.ErrorContext(ctx, "send telegram document", "err", err)
		return false
	}
	if !haveUser {
		return true
	}
	if msg == nil || msg.Document == nil {
		c.removeKeyboard(ctx, b, chatID, msg)
		return true
	}
	pages, width, height := c.fileFacts(ctx, data, filename, msg.Document.MimeType)
	c.cleaned.put(token, tgUser, cleanedCopy{
		user: user, sourceDoc: sourceDoc,
		file: docsbot.IncomingFile{
			Kind: docsbot.KindDocument, FileID: msg.Document.FileID, FileUniqueID: msg.Document.FileUniqueID,
			Title: docsbot.TrimExt(filename), FileSize: msg.Document.FileSize, MIME: msg.Document.MimeType,
			PageCount: pages, Width: width, Height: height,
		},
	})
	return true
}

// onKeepClean makes the cleaned copy the document's file: conv:kc:<token>. The
// copy is taken first, so a double tap acts once; when the document cannot be
// changed right now it is put back and the buttons return.
func (c *Converter) onKeepClean(ctx context.Context, b *bot.Bot, p press) {
	chatID, token := p.chatID, p.data.token()
	cp, ok := c.cleaned.take(token, p.tgUser)
	if !ok {
		c.out(b).Send(ctx, chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, p)
	retry := func(text string) {
		c.cleaned.put(token, p.tgUser, cp)
		c.out(b).SetKeyboard(ctx, chatID, p.msgID, cleanedKeyboard(token))
		c.out(b).Send(ctx, chatID, text)
	}

	rep, err := c.svc.ReplaceContent(ctx, cp.user, cp.sourceDoc, cp.file)
	if errors.Is(err, docsbot.ErrNotFound) {
		c.out(b).Send(ctx, chatID, textGone)
		return
	}
	if err != nil {
		c.log.ErrorContext(ctx, "keep cleaned copy", "err", err, "user_id", cp.user, "doc_id", cp.sourceDoc)
		retry(textError)
		return
	}
	switch r := rep.(type) {
	case docsbot.ReplaceBusy:
		retry(textReplaceBusy)
	case docsbot.ReplaceMissing:
		retry(textReplaceMissing)
	case docsbot.Replaced:
		c.showKept(ctx, b, chatID, cp.user, r.Doc, true)
	case docsbot.AlreadySaved:
		c.docs.SendAlreadySaved(ctx, b, chatID, cp.user, r.Doc.ID)
	default:
		c.docs.SendNotice(ctx, b, chatID, rep)
	}
}

// onKeepOriginal leaves the document as it is: conv:ko:<token>.
func (c *Converter) onKeepOriginal(ctx context.Context, b *bot.Bot, p press) {
	chatID := p.chatID
	cp, ok := c.cleaned.take(p.data.token(), p.tgUser)
	if !ok {
		c.out(b).Send(ctx, chatID, textConvertResultOld)
		return
	}
	c.clearButtons(ctx, b, p)
	doc, ok := c.loadDoc(ctx, b, chatID, cp.user, cp.sourceDoc)
	if !ok {
		return
	}
	c.showKept(ctx, b, chatID, cp.user, doc, false)
}

// showKept moves the user on after the choice: a document still waiting for its
// category gets the category picker, a saved one its card.
func (c *Converter) showKept(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, doc docsbot.Document, replaced bool) {
	switch {
	case doc.Status == docsbot.StatusPending:
		c.docs.SendNotice(ctx, b, chatID, docsbot.NeedCategory{Doc: doc})
	case replaced:
		c.docs.SendReplaced(ctx, b, chatID, user, doc.ID)
	default:
		c.docs.SendAlreadySaved(ctx, b, chatID, user, doc.ID)
	}
}

// logMetaFailure logs a failed metadata operation once. The error text of a
// parser can quote bytes of the user's file, so only its class is logged; a
// download failure is token-safe and logged whole.
func (c *Converter) logMetaFailure(ctx context.Context, what string, err error, attrs ...any) {
	if errors.Is(err, errDownload) {
		c.log.ErrorContext(ctx, what+": download failed", append(attrs, "err", err)...)
		return
	}
	c.log.WarnContext(ctx, what+" failed", append(attrs, "reason", metaFailure(err))...)
}

// metaFailure names why a metadata operation failed.
func metaFailure(err error) string {
	switch {
	case errors.Is(err, convert.ErrUnsupportedFormat):
		return "unsupported"
	case errors.Is(err, convert.ErrFileTooLarge):
		return "too_large"
	case errors.Is(err, convert.ErrEncrypted):
		return "encrypted"
	case errors.Is(err, convert.ErrTimeout):
		return "timeout"
	case errors.Is(err, convert.ErrUnreadable):
		return "unreadable"
	case errors.Is(err, errStillDirty):
		return "still_dirty"
	}
	return "other"
}

// infoErrorText is what the user is told about a failed metadata operation.
func infoErrorText(err error) string {
	switch {
	case errors.Is(err, errDownload):
		return textConvertDownloadFailed
	case errors.Is(err, convert.ErrUnsupportedFormat):
		return textInfoUnsupported
	case errors.Is(err, convert.ErrFileTooLarge):
		return textInfoTooLarge
	case errors.Is(err, convert.ErrEncrypted):
		return textInfoEncrypted
	case errors.Is(err, convert.ErrTimeout):
		return textPDFTimeout
	case errors.Is(err, errStillDirty):
		return textInfoCleanFailed
	default:
		return textInfoUnreadable
	}
}

// fileExt is the extension of a file of the given type.
func fileExt(t convert.FileType) string {
	if t == convert.FileJPEG {
		return "jpg"
	}
	return string(t)
}
