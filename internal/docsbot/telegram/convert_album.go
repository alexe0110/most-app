package telegram

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

// registerAlbum remembers an album's images for the "one PDF" button and returns
// the button's token.
func (c *converter) registerAlbum(user docsbot.UserID, group string, images []albumImage) string {
	sorted := slices.Clone(images)
	slices.SortFunc(sorted, func(a, b albumImage) int { return a.msgID - b.msgID })
	token := newToken()
	c.albums.put(token, convAlbum{user: user, group: group, images: sorted})
	return token
}

// onAlbum merges an album into one PDF: conv:alb:<token>. On success the album's
// pending documents are dropped, so they don't wait for a category.
func (c *converter) onAlbum(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	token := strings.TrimPrefix(update.CallbackQuery.Data, cbAlbumPrefix)
	user, ok := userID(ctx)
	if !ok {
		return
	}
	album, ok := c.albums.peek(token, user)
	if !ok {
		_, _ = c.out(b).send(ctx, chatID, textConvertResultOld)
		return
	}
	var declared int64
	for _, img := range album.images {
		declared += img.size
	}
	switch {
	case len(album.images) > maxAlbumPages:
		_, _ = c.out(b).send(ctx, chatID, fmt.Sprintf(textAlbumTooMany, maxAlbumPages)) // the buttons stay: save one by one
		return
	case declared > maxAlbumBytes:
		_, _ = c.out(b).send(ctx, chatID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20))
		return
	}
	album, ok = c.albums.take(token, user) // a double tap stops here
	if !ok {
		return
	}

	// The choice message becomes the progress note; an error puts its buttons back.
	var choice *models.InlineKeyboardMarkup
	if m := update.CallbackQuery.Message.Message; m != nil {
		choice = m.ReplyMarkup
	}
	msgID := c.show(ctx, b, chatID, callbackMessageID(update), textConvertProgress, nil)
	pages := make([][]byte, 0, len(album.images))
	var total int
	for _, img := range album.images {
		data, err := c.download(ctx, b, img.fileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download album image", "err", err, "user_id", user)
			c.albums.put(token, album) // let the user retry
			c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, choice)
			return
		}
		if total += len(data); total > maxAlbumBytes {
			c.albums.put(token, album)
			c.show(ctx, b, chatID, msgID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20), choice)
			return
		}
		pages = append(pages, data)
	}
	pdf, err := convert.ImagesToPDF(pages)
	if err != nil {
		c.log.ErrorContext(ctx, "convert album to pdf", "err", err, "user_id", user, "pages", len(pages))
		c.albums.put(token, album)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), choice)
		return
	}
	c.log.InfoContext(ctx, "album converted", "user_id", user, "pages", len(pages), "in_bytes", total, "out_bytes", len(pdf))

	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, update.CallbackQuery.From.ID, name, pdf, "", suffixPDF) {
		c.show(ctx, b, chatID, msgID, textSendFailed, choice)
		c.albums.put(token, album)
		return
	}
	c.finish(ctx, b, chatID, msgID)
	n, err := c.svc.DiscardAlbum(ctx, user, album.group)
	if err != nil {
		c.log.ErrorContext(ctx, "discard album", "err", err, "user_id", user)
		return
	}
	c.log.InfoContext(ctx, "album discarded", "user_id", user, "count", n)
}

// onSkip keeps only the file: conv:skip:<token>.
func (c *converter) onSkip(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	c.results.take(strings.TrimPrefix(update.CallbackQuery.Data, cbSkipPrefix), update.CallbackQuery.From.ID)
	c.clearButtons(ctx, b, update, chatID)
}
