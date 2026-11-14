package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// finishPDF renders the collected photos into a PDF. The session is claimed
// first, so a double «Готово» builds it once; it goes back when the user can
// retry. The dialogue lock covers the claim and the progress note, so a photo
// arriving at the same time can't move the dialogue under them.
func (c *converter) finishPDF(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64) {
	unlock := c.dialogue.lock(user)
	msgID := callbackMessageID(update)
	sess, ok := c.sessions.claim(user, msgID, stepCollectPDF)
	if !ok {
		unlock()
		c.stale(ctx, b, update, chatID)
		return
	}
	pending := sess.orderedImages()
	if len(pending) == 0 {
		unlock()
		c.retry(ctx, b, chatID, user, sess, msgID, textConvertNoImages, pdfKeyboard())
		return
	}
	msgID = c.show(ctx, b, chatID, msgID, textConvertProgress, nil)
	unlock()

	// The declared sizes are not trusted: the real total is checked as the pages arrive.
	// The downloads share one budget well under the handler timeout, so the error
	// below can still be shown on a live ctx when Telegram is slow.
	dlCtx, cancel := context.WithTimeout(ctx, photosDownloadBudget)
	defer cancel()
	images := make([][]byte, 0, len(pending))
	total := 0
	for _, img := range pending {
		data, err := c.download(dlCtx, b, img.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download photo", "err", err, "user_id", user)
			c.retry(ctx, b, chatID, user, sess, msgID, textConvertDownloadFailed, pdfKeyboard())
			return
		}
		if total += len(data); total > maxAlbumBytes {
			// The session is not restored: the same photos would fail the same way.
			c.show(ctx, b, chatID, msgID, fmt.Sprintf(textPhotosOverLimit, maxAlbumBytes>>20), nil)
			return
		}
		images = append(images, data)
	}

	pdf, err := convert.ImagesToPDF(images)
	if err != nil {
		c.log.ErrorContext(ctx, "convert images to pdf", "err", err, "user_id", user, "pages", len(images))
		c.retry(ctx, b, chatID, user, sess, msgID, convertErrorText(err), pdfKeyboard())
		return
	}

	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, user, name, pdf, "", suffixPDF) {
		c.retry(ctx, b, chatID, user, sess, msgID, textSendFailed, pdfKeyboard())
		return
	}
	c.finish(ctx, b, chatID, msgID)
}

// finishCompress compresses the stored image to the chosen format. The session
// is claimed first (one run per tap); it goes back when the user can retry.
func (c *converter) finishCompress(ctx context.Context, b *bot.Bot, update *models.Update, user, chatID int64, format convert.Format) {
	if !c.compress(ctx, b, chatID, user, callbackMessageID(update), format) {
		c.stale(ctx, b, update, chatID)
	}
}

// compress runs the compression for a session at the format step; pressMsg is the
// message a button was pressed on (0 when a typed size got here). It reports
// false if there was nothing to claim. Callers must not hold the dialogue lock.
func (c *converter) compress(ctx context.Context, b *bot.Bot, chatID, user int64, pressMsg int, format convert.Format) bool {
	unlock := c.dialogue.lock(user)
	sess, ok := c.sessions.claim(user, pressMsg, stepChooseFormat)
	if !ok {
		unlock()
		return false
	}
	if len(sess.source) == 0 {
		c.sessions.restore(user, sess)
		unlock()
		return false
	}
	src := sess.source
	maxBytes := sess.maxBytes
	msgID := c.show(ctx, b, chatID, sess.msgID, textConvertProgress, nil)
	unlock()

	// An error puts the user back at the last question that was asked.
	retryKB := formatKeyboard()
	if !sess.inputPNG {
		retryKB = limitKeyboard()
		sess.step = stepChooseLimit
	}
	out, err := convert.Compress(src, maxBytes, format)
	if err != nil {
		c.log.ErrorContext(ctx, "convert compress", "err", err)
		c.retry(ctx, b, chatID, user, sess, msgID, convertErrorText(err), retryKB)
		return true
	}

	if len(out) > maxBytes {
		c.out(b).send(ctx, chatID, fmt.Sprintf(textConvertBestEffort, len(out)/1_024))
	}
	name := resultFileName(sess.sourceTitle, nameFallbackPhoto, nameTagCompressed, formatExt(format))
	if !c.sendResult(ctx, b, chatID, user, name, out, "", suffixCompressed) {
		c.retry(ctx, b, chatID, user, sess, msgID, textSendFailed, retryKB)
		return true
	}
	c.finish(ctx, b, chatID, msgID)
	return true
}

// scanName is the file name of a PDF made from photos: "Скан 10.11.2026.pdf".
func scanName(now time.Time) string { return fmt.Sprintf(nameScan, now.Format("02.01.2006")) }
