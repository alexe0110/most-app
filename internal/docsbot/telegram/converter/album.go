package converter

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// AlbumImage is one image of an album offered as a PDF. MsgID keeps the page
// order: album photos arrive as separate updates in any order. Only the file id
// and the declared size are kept, the bytes are downloaded when the PDF is built.
type AlbumImage struct {
	MsgID  int
	FileID string
	Size   int64
}

// RegisterAlbum remembers an album's images for the "one PDF" button and returns
// the button's token.
func (c *Converter) RegisterAlbum(user docsbot.UserID, tgUser int64, group string, images []AlbumImage) string {
	sorted := slices.Clone(images)
	slices.SortFunc(sorted, func(a, b AlbumImage) int { return a.MsgID - b.MsgID })
	token := tg.NewToken()
	c.albums.put(token, tgUser, convAlbum{user: user, group: group, images: sorted})
	return token
}

// onAlbum merges an album into one PDF: conv:alb:<token>. On success the album's
// pending documents are dropped, so they don't wait for a category.
func (c *Converter) onAlbum(ctx context.Context, b *bot.Bot, p press) {
	chatID, token, tgUser := p.chatID, p.data.token(), p.tgUser
	if !p.hasUser {
		return
	}
	user := p.user
	album, ok := c.albums.peek(token, tgUser)
	if !ok {
		c.out(b).Send(ctx, chatID, textConvertResultOld)
		return
	}
	var declared int64
	for _, img := range album.images {
		declared += img.Size
	}
	switch {
	case len(album.images) > maxAlbumPages:
		c.out(b).Send(ctx, chatID, fmt.Sprintf(textAlbumTooMany, maxAlbumPages)) // the buttons stay: save one by one
		return
	case declared > maxAlbumBytes:
		c.out(b).Send(ctx, chatID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20))
		return
	}
	album, ok = c.albums.take(token, tgUser) // a double tap stops here
	if !ok {
		return
	}

	// The choice message becomes the progress note; an error puts its buttons back.
	var choice *models.InlineKeyboardMarkup
	if p.msg != nil {
		choice = p.msg.ReplyMarkup
	}
	msgID := c.show(ctx, b, chatID, p.msgID, textConvertProgress, nil)
	pages := make([][]byte, 0, len(album.images))
	var total int
	for _, img := range album.images {
		data, err := c.download(ctx, img.FileID)
		if err != nil {
			c.log.ErrorContext(ctx, "convert download album image", "err", err, "user_id", user)
			c.albums.put(token, tgUser, album) // let the user retry
			c.show(ctx, b, chatID, msgID, textConvertDownloadFailed, choice)
			return
		}
		if total += len(data); total > maxAlbumBytes {
			c.albums.put(token, tgUser, album)
			c.show(ctx, b, chatID, msgID, fmt.Sprintf(textAlbumTooBig, maxAlbumBytes>>20), choice)
			return
		}
		pages = append(pages, data)
	}
	ictx, cancelImage := context.WithTimeout(ctx, imageTimeout)
	pdf, err := convert.ImagesToPDF(ictx, pages)
	cancelImage()
	if err != nil {
		c.log.ErrorContext(ctx, "convert album to pdf", "err", err, "user_id", user, "pages", len(pages))
		c.albums.put(token, tgUser, album)
		c.show(ctx, b, chatID, msgID, convertErrorText(err), choice)
		return
	}
	c.log.InfoContext(ctx, "album converted", "user_id", user, "pages", len(pages), "in_bytes", total, "out_bytes", len(pdf))

	name := scanName(c.now())
	if !c.sendResult(ctx, b, chatID, tgUser, name, pdf, "", suffixPDF) {
		c.show(ctx, b, chatID, msgID, textSendFailed, choice)
		c.albums.put(token, tgUser, album)
		return
	}
	c.done(ctx, docsbot.ConvertAlbumPDF)
	c.finish(ctx, b, chatID, msgID)
	n, err := c.svc.DiscardAlbum(ctx, user, album.group)
	if err != nil {
		c.log.ErrorContext(ctx, "discard album", "err", err, "user_id", user)
		return
	}
	c.log.InfoContext(ctx, "album discarded", "user_id", user, "count", n)
}

// onSkip keeps only the file: conv:skip:<token>.
func (c *Converter) onSkip(ctx context.Context, b *bot.Bot, p press) {
	c.results.take(p.data.token(), p.tgUser)
	c.clearButtons(ctx, b, p)
}
