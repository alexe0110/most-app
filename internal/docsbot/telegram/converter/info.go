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

// notInspectableText explains why the screen has nothing to show for a document
// that was offered the button earlier: it may have been over the limit already,
// the check being about the stored size.
func notInspectableText(doc docsbot.Document) string {
	if doc.Kind == docsbot.KindDocument && doc.FileSize > tg.MaxFileBytes {
		return infoTooLargeText()
	}
	return textInfoUnsupported
}

// showWorking turns the pressed message into the progress note and takes its
// buttons away, so nothing can be pressed while the work runs. If the edit fails
// the note goes out as a new message.
func (c *Converter) showWorking(ctx context.Context, b *bot.Bot, chatID int64, msgID int) int {
	if err := c.out(b).TryEdit(ctx, chatID, msgID, textConvertProgress, tg.WithKeyboard(nil)); err == nil {
		return msgID
	}
	return c.show(ctx, b, chatID, msgID, textConvertProgress, nil)
}

// metaTimeout bounds one metadata operation (reading, cleaning, checking the
// copy: each gets its own). A PDF takes no longer than an image here: pdfTimeout
// and imageTimeout are the same 20 seconds, so one value serves both.
const metaTimeout = imageTimeout

// canClean reports whether cleaning would change anything and can be done.
func canClean(info convert.FileInfo) bool {
	return !info.Clean() && !info.Encrypted
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
		out.Send(ctx, chatID, notInspectableText(doc), tg.WithKeyboard(back))
		return
	}
	_, info, err := c.loadInfo(ctx, doc)
	if err != nil {
		c.logMetaFailure(ctx, "inspect file", err, "user_id", p.user, "doc_id", id)
		out.Send(ctx, chatID, infoErrorText(err), tg.WithKeyboard(back))
		return
	}
	c.log.InfoContext(ctx, "file inspected", "user_id", p.user, "doc_id", id, "format", info.Type,
		"bytes", info.Size, "clean", info.Clean(), "fields", info.FieldCount())
	text, cut := renderInfo(doc.Title, info)
	shown := out.Send(ctx, chatID, text,
		tg.WithHTML(), tg.WithoutPreview(), tg.WithKeyboard(infoKeyboard(id, canClean(info))))
	if shown == 0 {
		return
	}
	c.track(ctx, docsbot.EventFileInfo, info.Type)
	if cut {
		c.sendFieldList(ctx, b, chatID, doc.Title, info)
	}
}

// track records a file_info or file_cleaned event. The format is the only prop:
// no metadata value or file name ever reaches an event.
func (c *Converter) track(ctx context.Context, name docsbot.EventName, t convert.FileType) {
	if user, ok := userID(ctx); ok {
		c.svc.Track(ctx, user, name, docsbot.EventProps{Format: fileFormat(t)})
	}
}

// fileFormat maps a detected file type to its event value.
func fileFormat(t convert.FileType) docsbot.FileFormat {
	switch t {
	case convert.FileJPEG:
		return docsbot.FormatJPEG
	case convert.FilePNG:
		return docsbot.FormatPNG
	case convert.FileWebP:
		return docsbot.FormatWebP
	case convert.FileHEIC:
		return docsbot.FormatHEIC
	case convert.FilePDF:
		return docsbot.FormatPDF
	case convert.FileDOCX:
		return docsbot.FormatDOCX
	case convert.FileXLSX:
		return docsbot.FormatXLSX
	case convert.FilePPTX:
		return docsbot.FormatPPTX
	}
	return ""
}

// loadInfo downloads the document and reads its metadata, under metaTimeout.
func (c *Converter) loadInfo(ctx context.Context, doc docsbot.Document) ([]byte, convert.FileInfo, error) {
	data, err := c.download(ctx, doc.TgFileID)
	if err != nil {
		return nil, convert.FileInfo{}, fmt.Errorf("%w: %w", errDownload, err)
	}
	ictx, cancel := context.WithTimeout(ctx, metaTimeout)
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
		c.out(b).Send(ctx, chatID, notInspectableText(doc))
		return
	}
	back := infoKeyboard(id, false)
	msgID := c.showWorking(ctx, b, chatID, p.msgID)

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
	cleaned, after, err := c.strip(ctx, data)
	if err != nil {
		c.logMetaFailure(ctx, "clean file", err, "user_id", p.user, "doc_id", id, "format", before.Type)
		c.show(ctx, b, chatID, msgID, infoErrorText(err), infoKeyboard(id, true))
		return
	}

	name := resultFileName(doc.Title, nameFallbackDocument, nameTagClean, fileExt(before.Type))
	file := cleanedFile{name: name, caption: cleanedCaption(before, after), data: cleaned, sourceDoc: id}
	if !c.sendCleaned(ctx, b, chatID, p.tgUser, file) {
		c.show(ctx, b, chatID, msgID, textSendFailed, c.reportable(textSendFailed, infoKeyboard(id, true), scenarioInfo))
		return
	}
	c.log.InfoContext(ctx, "file cleaned", "user_id", p.user, "doc_id", id, "format", before.Type,
		"in_bytes", len(data), "out_bytes", len(cleaned))
	c.track(ctx, docsbot.EventFileCleaned, before.Type)
	c.finish(ctx, b, chatID, msgID)
}

// strip removes the metadata of a file and checks the result: a copy
// that still reads as dirty is never offered, so «чистый» is never a promise the
// file does not keep. Cleaning and the check each get their own timeout. The
// second result is what the check found in the copy.
func (c *Converter) strip(ctx context.Context, data []byte) ([]byte, convert.FileInfo, error) {
	sctx, cancel := context.WithTimeout(ctx, metaTimeout)
	defer cancel()
	cleaned, err := convert.Strip(sctx, data)
	if err != nil {
		return nil, convert.FileInfo{}, err
	}
	cctx, cancelCheck := context.WithTimeout(ctx, metaTimeout)
	defer cancelCheck()
	after, err := convert.Inspect(cctx, cleaned)
	if err != nil {
		return nil, convert.FileInfo{}, err
	}
	if !after.Clean() {
		return nil, convert.FileInfo{}, errStillDirty
	}
	return cleaned, after, nil
}

// cleanedFile is a cleaned copy ready to be sent.
type cleanedFile struct {
	name, caption string
	data          []byte
	sourceDoc     docsbot.DocumentID
}

// sendCleaned uploads the cleaned copy with the keep-which buttons and reports
// whether Telegram accepted it. As in sendResult the choice is remembered only if
// Telegram echoes the stored file back.
func (c *Converter) sendCleaned(ctx context.Context, b *bot.Bot, chatID, tgUser int64, f cleanedFile) bool {
	params := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: f.name, Data: bytes.NewReader(f.data)},
		Caption:  f.caption,
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
	pages, width, height := c.fileFacts(ctx, f.data, f.name, msg.Document.MimeType)
	c.cleaned.put(token, tgUser, cleanedCopy{
		user: user, sourceDoc: f.sourceDoc,
		file: docsbot.IncomingFile{
			Kind: docsbot.KindDocument, FileID: msg.Document.FileID, FileUniqueID: msg.Document.FileUniqueID,
			Title: docsbot.TrimExt(f.name), FileSize: msg.Document.FileSize, MIME: msg.Document.MimeType,
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
		c.out(b).Send(ctx, chatID, text, tg.WithKeyboard(c.reportable(text, nil, scenarioInfo)))
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

// fileExt is the extension of a file of the given type.
func fileExt(t convert.FileType) string {
	if t == convert.FileJPEG {
		return "jpg"
	}
	return string(t)
}
