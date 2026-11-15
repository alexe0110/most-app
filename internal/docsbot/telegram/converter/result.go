package converter

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// clearButtons removes the keyboard from the message a button was pressed on.
func (c *Converter) clearButtons(ctx context.Context, b *bot.Bot, p press) {
	if p.hasMsg {
		c.out(b).ClearKeyboard(ctx, p.chatID, p.msgID)
	}
}

// deleteCallbackMessage removes the message a button was pressed on.
func (c *Converter) deleteCallbackMessage(ctx context.Context, b *bot.Bot, p press) {
	if p.hasMsg {
		c.out(b).Delete(ctx, p.chatID, p.msgID)
	}
}

// sendResult uploads the converted file with the save / just-the-file buttons
// and reports whether Telegram accepted it. The choice is remembered only if
// Telegram echoes the stored file back; if it doesn't, the buttons are removed
// again, since they could not work. Without the user's internal id the file goes
// out without buttons.
func (c *Converter) sendResult(ctx context.Context, b *bot.Bot, chatID, tgUser int64, filename string, data []byte,
	sourceDoc docsbot.DocumentID, suffix string,
) bool {
	params := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
	}
	user, haveUser := userID(ctx)
	token := tg.NewToken()
	if haveUser {
		params.ReplyMarkup = saveKeyboard(token, sourceDoc != "")
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
	c.results.put(token, tgUser, convResult{
		user: user, sourceDoc: sourceDoc, suffix: suffix,
		file: docsbot.IncomingFile{
			Kind: docsbot.KindDocument, FileID: msg.Document.FileID, FileUniqueID: msg.Document.FileUniqueID,
			Title: docsbot.TrimExt(filename), FileSize: msg.Document.FileSize, MIME: msg.Document.MimeType, PageCount: pages,
			Width: width, Height: height,
		},
	})
	return true
}

// fileFacts reads what a stored copy of data should record: the page count of a
// PDF and the pixel size of an image; zero when unknown.
func (c *Converter) fileFacts(ctx context.Context, data []byte, filename, mime string) (pages, width, height int) {
	if w, h, ok := convert.ImageSize(data); ok { // header only; other formats stay unknown
		width, height = w, h
	}
	if mime == mimePDF || strings.EqualFold(filepath.Ext(filename), ".pdf") {
		// The result is already in memory; a failed count only leaves the page count unknown.
		pctx, cancel := context.WithTimeout(ctx, pdfTimeout)
		defer cancel()
		if n, err := convert.PageCount(pctx, data); err == nil {
			pages = n
		}
	}
	return pages, width, height
}

// done records a finished conversion. Call it once Telegram accepted the result,
// so a failed send, which the user retries, is not counted twice. The operation is
// one of the fixed docsbot.ConvertOp values, never anything the user typed.
func (c *Converter) done(ctx context.Context, op docsbot.ConvertOp) {
	if user, ok := userID(ctx); ok {
		c.svc.Track(ctx, user, docsbot.EventConvertDone, docsbot.EventProps{Operation: op})
	}
}

// quickConvertOp maps a quick operation of a stored image to its event name.
func quickConvertOp(op string) docsbot.ConvertOp {
	switch op {
	case opPDF:
		return docsbot.ConvertPhotoPDF
	case opJPG:
		return docsbot.ConvertWebPJPG
	default:
		return docsbot.ConvertCompress
	}
}

// pdfConvertOp maps a PDF operation to its event name.
func pdfConvertOp(op string) docsbot.ConvertOp {
	switch op {
	case opRotate:
		return docsbot.ConvertPDFRotate
	case opKeep:
		return docsbot.ConvertPDFKeep
	case opDrop:
		return docsbot.ConvertPDFRemove
	default:
		return docsbot.ConvertPDFMerge
	}
}

// removeKeyboard strips the buttons from a sent message, when there is one.
func (c *Converter) removeKeyboard(ctx context.Context, b *bot.Bot, chatID int64, msg *models.Message) {
	if msg != nil {
		c.out(b).ClearKeyboard(ctx, chatID, msg.ID)
	}
}

// download fetches a Telegram file's bytes; the cap and the token-safe errors
// belong to the downloader.
func (c *Converter) download(ctx context.Context, fileID string) ([]byte, error) {
	data, _, err := c.files.Download(ctx, fileID)
	return data, err
}

// resultFileName is the file name of a converted document: the title without its
// extension (fallback when there is none), a dash, the tag and the extension:
// "Договор-сжатый.jpg". Path separators are replaced so the name is a single path
// element.
func resultFileName(title, fallback, tag, ext string) string {
	base := docsbot.DerivedTitle(title, "") // drops the extension, trims the length
	if base == "" {
		base = fallback
	}
	base = strings.NewReplacer("/", "_", "\\", "_").Replace(base)
	return base + "-" + tag + "." + ext
}
