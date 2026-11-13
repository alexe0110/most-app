package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// onExport is the "send all documents" button.
func (d *docs) onExport(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, ok := callbackChat(u)
	if !ok {
		return
	}
	// The button is spent: a second tap must not start another export.
	if _, msgID, ok := callbackMessage(u); ok {
		d.editMarkup(ctx, b, chatID, msgID, &models.InlineKeyboardMarkup{})
	}
	d.export(ctx, b, chatID, user)
}

// exportCmd is the /export command.
func (d *docs) exportCmd(ctx context.Context, b *bot.Bot, u *models.Update) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	d.export(ctx, b, u.Message.Chat.ID, user)
}

// export resends the user's stored documents from Telegram, up to the service's
// per-request cap: photos and documents go in albums of up to maxAlbumFiles, paced
// under the Bot API rate limit.
func (d *docs) export(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID) {
	if !d.startExport(user) {
		reply(ctx, b, d.log, chatID, textExportRunning)
		return
	}
	defer d.finishExport(user)
	exp, err := d.svc.ExportAll(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "export", err)
		return
	}
	if exp.Total == 0 {
		reply(ctx, b, d.log, chatID, textExportEmpty)
		return
	}
	d.log.InfoContext(ctx, "export started", "user_id", user, "count", len(exp.Docs))
	reply(ctx, b, d.log, chatID, textExportStart)
	sent, prev := 0, 0
	for i, batch := range exportBatches(exp.Docs) {
		if i > 0 && !pause(ctx, d.exportGap(prev)) {
			return // context cancelled; stop sending
		}
		prev = len(batch)
		if len(batch) == 1 {
			if d.sendFile(ctx, b, chatID, batch[0]) {
				sent++
			}
			continue
		}
		sent += d.sendAlbum(ctx, b, chatID, batch)
	}
	if exp.Truncated {
		reply(ctx, b, d.log, chatID, fmt.Sprintf(textExportTruncated, sent, exp.Total))
		return
	}
	reply(ctx, b, d.log, chatID, fmt.Sprintf(textExportDone, sent))
}

// exportGap is the pause after a batch of n files. Telegram lets a bot send about
// one message a second into a chat, and an album of n counts as n, so the pause
// grows with the album; a single file only needs the short one.
func (d *docs) exportGap(n int) time.Duration {
	if n <= 1 {
		return d.pauseFile
	}
	return d.pauseAlbum * time.Duration(n)
}

// startExport marks the user's export as running; false if one already is.
func (d *docs) startExport(user docsbot.UserID) bool {
	d.exportsMu.Lock()
	defer d.exportsMu.Unlock()
	if d.exports[user] {
		return false
	}
	d.exports[user] = true
	return true
}

func (d *docs) finishExport(user docsbot.UserID) {
	d.exportsMu.Lock()
	defer d.exportsMu.Unlock()
	delete(d.exports, user)
}

// maxRetryAfter caps how long one rate-limit wait may last.
const maxRetryAfter = 60 * time.Second

// retryOnRateLimit runs send and, when Telegram answers 429, waits the retry_after
// it asks for (capped) and runs it once more.
func (d *docs) retryOnRateLimit(ctx context.Context, send func() error) error {
	err := send()
	var limited *bot.TooManyRequestsError
	if !errors.As(err, &limited) {
		return err
	}
	wait := min(time.Duration(limited.RetryAfter)*time.Second, maxRetryAfter)
	d.log.WarnContext(ctx, "telegram rate limit", "retry_after_s", limited.RetryAfter)
	if !pause(ctx, wait) {
		return fmt.Errorf("wait for rate limit: %w", ctx.Err())
	}
	return send()
}

// maxAlbumFiles is the most files Telegram takes in one media group.
const maxAlbumFiles = 10

// exportBatches splits documents, in order, into runs of one kind (photo or
// document: Telegram does not mix them in an album) of at most maxAlbumFiles.
func exportBatches(docs []docsbot.Document) [][]docsbot.Document {
	var out [][]docsbot.Document
	for _, doc := range docs {
		if n := len(out); n > 0 {
			last := out[n-1]
			if len(last) < maxAlbumFiles && (last[0].Kind == docsbot.KindPhoto) == (doc.Kind == docsbot.KindPhoto) {
				out[n-1] = append(last, doc)
				continue
			}
		}
		out = append(out, []docsbot.Document{doc})
	}
	return out
}

// sendAlbum resends 2-10 stored files of one kind as a media group by file_id and
// returns how many went out (0 on failure).
func (d *docs) sendAlbum(ctx context.Context, b *bot.Bot, chatID int64, batch []docsbot.Document) int {
	media := make([]models.InputMedia, 0, len(batch))
	for _, doc := range batch {
		caption := doc.Title + " · " + doc.Category.Label()
		if doc.Kind == docsbot.KindPhoto {
			media = append(media, &models.InputMediaPhoto{Media: doc.TgFileID, Caption: caption})
		} else {
			media = append(media, &models.InputMediaDocument{Media: doc.TgFileID, Caption: caption})
		}
	}
	start := time.Now()
	err := d.retryOnRateLimit(ctx, func() error {
		_, err := b.SendMediaGroup(ctx, &bot.SendMediaGroupParams{ChatID: chatID, Media: media})
		return err
	})
	traceAPI(ctx, d.log, "sendMediaGroup", start)
	if err != nil {
		d.log.ErrorContext(ctx, "send album", "err", err, "user_id", batch[0].UserID, "count", len(batch))
		reply(ctx, b, d.log, chatID, textSendFailed)
		return 0
	}
	d.log.InfoContext(ctx, "album sent", "user_id", batch[0].UserID, "count", len(batch))
	return len(batch)
}

// exportPause follows a single resent file; after an album the pause is
// exportPerFilePause per file in it (see exportGap).
const (
	exportPause        = 50 * time.Millisecond
	exportPerFilePause = time.Second
)

// pause waits for d, returning false if ctx is cancelled first.
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// IsExport reports an update that starts the export of all documents. The export
// is paced under the Bot API rate limit and capped by the service, so it can
// outlast a regular handler budget and runs without one.
func IsExport(u *models.Update) bool {
	if cb := u.CallbackQuery; cb != nil {
		return cb.Data == cbPrefix+actExport
	}
	if m := u.Message; m != nil {
		cmd, _, _ := strings.Cut(m.Text, " ")
		cmd, _, _ = strings.Cut(cmd, "@")
		return cmd == "/export"
	}
	return false
}
