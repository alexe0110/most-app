package telegram

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// onDocument accepts image documents (and PDFs while merging) and rejects the rest.
func (c *converter) onDocument(ctx context.Context, b *bot.Bot, update *models.Update) {
	doc := update.Message.Document
	if doc.MimeType == mimePDF {
		c.onPDFPart(ctx, b, update)
		return
	}
	user, _ := messageUser(update)
	if sess, ok := c.sessions.live(user); ok && awaitsPDF(sess.step) {
		_, _ = c.out(b).send(ctx, update.Message.Chat.ID, textPDFNotPDF)
		return
	}
	if !supportedImageMIME(doc.MimeType) {
		text := textConvertUnsupported
		if unsupportedImage(doc.MimeType, doc.FileName) {
			text = textConvertFormatNotSupported
		}
		_, _ = c.out(b).send(ctx, update.Message.Chat.ID, text)
		return
	}
	c.onImage(ctx, b, update, doc.FileID, doc.FileSize)
}

// onImage adds an image to the current step. For a PDF only its id and size are
// kept; compress downloads it now, since the format question depends on its bytes.
func (c *converter) onImage(ctx context.Context, b *bot.Bot, update *models.Update, fileID string, size int64) {
	user, _ := messageUser(update)
	chatID := update.Message.Chat.ID

	sess, ok := c.sessions.live(user)
	if !ok {
		return
	}
	switch sess.step {
	case stepCollectPDF:
		c.addPage(ctx, b, update, fileID, size)
		return
	case stepAwaitImage:
	default:
		_, _ = c.out(b).send(ctx, chatID, textConvertTapButton)
		return
	}

	data, err := c.download(ctx, b, fileID)
	if err != nil {
		c.log.ErrorContext(ctx, "convert download image", "err", err)
		_, _ = c.out(b).send(ctx, chatID, textConvertDownloadFailed)
		return
	}

	unlock := c.dialogue.lock(user)
	defer unlock()
	// Two images sent in a row both pass the check above while downloading: only
	// the first one moves the dialogue on, the other is dropped.
	advanced := false
	c.sessions.update(user, func(s *session) {
		if s.step != stepAwaitImage {
			return
		}
		s.source = data
		s.step = stepChooseLimit
		s.inputPNG = isPNG(data)
		s.sourceTitle = ""
		if d := update.Message.Document; d != nil {
			s.sourceTitle = d.FileName
		}
		advanced = true
	})
	if advanced {
		c.moveDown(ctx, b, chatID, user, textConvertChooseLimit+c.fileTip(user, update), limitKeyboard())
	}
}

// addPage adds a photo to a PDF in progress without downloading it. The limits
// are those of an album PDF; a photo over them is refused and the session goes on.
func (c *converter) addPage(ctx context.Context, b *bot.Bot, update *models.Update, fileID string, size int64) {
	user, _ := messageUser(update)
	chatID := update.Message.Chat.ID

	unlock := c.dialogue.lock(user)
	defer unlock()
	pages, refusal := 0, ""
	c.sessions.update(user, func(s *session) {
		switch {
		case s.step != stepCollectPDF:
		case len(s.images) >= maxAlbumPages:
			refusal = fmt.Sprintf(textPhotosTooMany, maxAlbumPages)
		case s.imageSum+size > maxAlbumBytes:
			refusal = fmt.Sprintf(textPhotosTooBig, maxAlbumBytes>>20)
		default:
			s.images = append(s.images, pendingImage{msgID: update.Message.ID, fileID: fileID, size: size})
			s.imageSum += size
			pages = len(s.images)
		}
	})
	switch {
	case refusal != "":
		_, _ = c.out(b).send(ctx, chatID, refusal)
	case pages > 0 && !c.dialogue.busy(user):
		// Photos of an album arrive in parallel: only the last one waiting shows
		// the total, so the chat gets one message, not one per photo.
		text := fmt.Sprintf(textConvertPDFAccepted, pages) + c.fileTip(user, update)
		c.moveDown(ctx, b, chatID, user, text, pdfKeyboard())
	}
}

// fileTip is the "send as file" tip for a user who just sent a compressed photo
// (not a file). It is returned once per user per process, "" afterwards.
func (c *converter) fileTip(user int64, update *models.Update) string {
	if len(update.Message.Photo) == 0 {
		return ""
	}
	c.tipMu.Lock()
	defer c.tipMu.Unlock()
	if c.tipShown[user] {
		return ""
	}
	c.tipShown[user] = true
	return "\n\n" + textConvertFileTip
}

// pngMagic opens every PNG file.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func isPNG(data []byte) bool { return bytes.HasPrefix(data, pngMagic) }

// awaitsPDF reports whether the step waits for a PDF file from the user.
func awaitsPDF(s step) bool { return s == stepMergePDF || s == stepAwaitPDF }

// onText reads a typed custom size; other text just nudges to tap a button.
func (c *converter) onText(ctx context.Context, b *bot.Bot, update *models.Update, at step) {
	chatID := update.Message.Chat.ID
	if at == stepAwaitRange {
		c.onRange(ctx, b, update)
		return
	}
	if at != stepCustomLimit {
		_, _ = c.out(b).send(ctx, chatID, textConvertTapButton)
		return
	}

	kb, ok := parseLimitKB(update.Message.Text)
	if !ok {
		_, _ = c.out(b).send(ctx, chatID, textConvertBadNumber)
		return
	}
	user, _ := messageUser(update)
	unlock := sync.OnceFunc(c.dialogue.lock(user))
	defer unlock()
	sess, ok := c.sessions.advance(user, 0, []step{stepCustomLimit}, func(s *session) {
		s.maxBytes = kb * 1_024
		s.step = stepChooseFormat
	})
	if !ok {
		return
	}
	if !sess.inputPNG {
		unlock() // compress takes the lock itself
		c.compress(ctx, b, chatID, user, 0, convert.JPEG)
		return
	}
	c.moveDown(ctx, b, chatID, user, textConvertChooseFormat, formatKeyboard())
}
