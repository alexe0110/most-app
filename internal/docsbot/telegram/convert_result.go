package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// clearButtons removes the keyboard from the message a button was pressed on.
func (c *converter) clearButtons(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	if m := update.CallbackQuery.Message.Message; m != nil {
		c.out(b).clearKeyboard(ctx, chatID, m.ID)
	}
}

// deleteCallbackMessage removes the message a button was pressed on.
func (c *converter) deleteCallbackMessage(ctx context.Context, b *bot.Bot, update *models.Update, chatID int64) {
	if m := update.CallbackQuery.Message.Message; m != nil {
		c.out(b).delete(ctx, chatID, m.ID)
	}
}

// sendResult uploads the converted file with the save / just-the-file buttons
// and reports whether Telegram accepted it. The choice is remembered only if
// Telegram echoes the stored file back; if it doesn't, the buttons are removed
// again, since they could not work. Without the user's internal id the file goes
// out without buttons.
func (c *converter) sendResult(ctx context.Context, b *bot.Bot, chatID, tgUser int64, filename string, data []byte,
	sourceDoc docsbot.DocumentID, suffix string,
) bool {
	params := &bot.SendDocumentParams{
		ChatID:   chatID,
		Document: &models.InputFileUpload{Filename: filename, Data: bytes.NewReader(data)},
	}
	user, haveUser := userID(ctx)
	token := newToken()
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
	pages, width, height := 0, 0, 0
	if w, h, ok := convert.ImageSize(data); ok { // header only; other formats stay unknown
		width, height = w, h
	}
	if msg.Document.MimeType == "application/pdf" || strings.EqualFold(filepath.Ext(filename), ".pdf") {
		// The result is already in memory; a failed count only leaves the page count unknown.
		if n, err := convert.PageCount(ctx, data); err == nil {
			pages = n
		}
	}
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

// removeKeyboard strips the buttons from a sent message, when there is one.
func (c *converter) removeKeyboard(ctx context.Context, b *bot.Bot, chatID int64, msg *models.Message) {
	if msg != nil {
		c.out(b).clearKeyboard(ctx, chatID, msg.ID)
	}
}

// download fetches a Telegram file's bytes, capped at tg.MaxFileBytes.
func (c *converter) download(ctx context.Context, b *bot.Bot, fileID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadWait)
	defer cancel()

	file, err := b.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("get file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.FileDownloadLink(file), nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", stripURL(err))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", stripURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, tg.MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) > tg.MaxFileBytes {
		return nil, errors.New("file exceeds download limit")
	}
	return data, nil
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
