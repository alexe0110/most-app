package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// onSetCategory changes an existing document's category.
func (d *docs) onSetCategory(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, cat, ok := docCategory(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	card, err := d.svc.Recategorize(ctx, user, id, cat)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "recategorize", err)
		return
	}
	d.log.InfoContext(ctx, "document recategorized", "user_id", user, "doc_id", card.Doc.ID, "category", cat)
	orig := d.origins.get(chatID, msgID)
	if card.FileInOldFolder {
		d.editCardText(ctx, b, chatID, msgID, cardText(card.Doc)+"\n"+textDriveFileStayed, d.cardKB(ctx, card.Doc, "", orig), orig)
		return
	}
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}

// onChangeCat swaps a card's buttons for the category picker.
func (d *docs) onChangeCat(ctx context.Context, b *bot.Bot, u *models.Update, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	_ = d.out(b).setKeyboard(ctx, chatID, msgID, categoryKeyboard(actSetCategory, id))
}

// onSend resends the original file from Telegram by its file_id.
func (d *docs) onSend(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, ok := callbackChat(u)
	if !ok {
		return
	}
	card, err := d.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		_, _ = d.out(b).send(ctx, chatID, textGone)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	d.sendFile(ctx, b, chatID, card.Doc)
}

// onRenamePrompt arms the rename and turns the card's own message into the
// prompt, with a cancel button. Typing the name is the next text message.
func (d *docs) onRenamePrompt(ctx context.Context, b *bot.Bot, u *models.Update, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.renames.set(u.CallbackQuery.From.ID, id, chatID, msgID)
	_ = d.out(b).edit(ctx, chatID, msgID, textRenamePrompt, withHTML(), withKeyboard(renameCancelKeyboard(id)))
}

// onRenameCancel drops the rename armed on this message, if any, and puts the
// card back. Repeating it, or pressing it on a stale prompt, only redraws the card.
func (d *docs) onRenameCancel(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.renames.drop(u.CallbackQuery.From.ID, chatID, msgID)
	d.restoreCard(ctx, b, user, chatID, msgID, id)
}

// releaseRename ends the user's armed rename when they press a button on another
// message and returns that message to the card it was.
func (d *docs) releaseRename(ctx context.Context, b *bot.Bot, user docsbot.UserID, u *models.Update) {
	e, ok := d.renames.take(u.CallbackQuery.From.ID)
	if !ok {
		return
	}
	if chatID, msgID, ok := callbackMessage(u); ok && e.chat == chatID && e.msg == msgID {
		return
	}
	d.restoreCard(ctx, b, user, e.chat, e.msg, e.id)
}

// restoreCard redraws a message as the document's card, keeping where it was
// opened from. A vanished document turns the message into the "gone" note.
func (d *docs) restoreCard(ctx context.Context, b *bot.Bot, user docsbot.UserID, chatID int64, msgID int, id docsbot.DocumentID) {
	card, err := d.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.log.ErrorContext(ctx, "restore card", "err", err, "user_id", user)
		return
	}
	orig := d.origins.get(chatID, msgID)
	d.showCard(ctx, b, user, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}

// showCard puts a document's card on a message. A message that is the live status
// message of the document gets the status rendering instead, so its retry row and
// storage line stay; anything else gets the plain card.
func (d *docs) showCard(ctx context.Context, b *bot.Bot, user docsbot.UserID, chatID int64, msgID int, doc docsbot.Document, folderID, orig string) {
	if view, err := d.svc.StatusView(ctx, user, doc.ID); err == nil && view.Grouped &&
		view.Message.ChatID == chatID && view.Message.MessageID == msgID {
		text, kb := renderStatusMessage(view.Message, view.Lines, view.FolderByCat)
		_ = d.out(b).edit(ctx, chatID, msgID, text, withHTML(), withKeyboard(kb))
		return
	}
	d.editCard(ctx, b, chatID, msgID, doc, folderID, orig)
}

// applyRename takes the typed name: the user's message is removed (the chat stays
// clean), then the prompt message becomes the renamed card, or shows a hint and
// keeps waiting when the name is unusable.
func (d *docs) applyRename(ctx context.Context, b *bot.Bot, user docsbot.UserID, e renameEntry, m *models.Message) {
	// Cosmetic: a failure (too old, no right) is no reason to fail the rename.
	out := d.out(b)
	out.logFailure(ctx, slog.LevelWarn, "delete user message", out.delete(ctx, m.Chat.ID, m.ID, quiet()))
	card, err := d.svc.Rename(ctx, user, e.id, m.Text)
	switch {
	case errors.Is(err, docsbot.ErrEmptyTitle):
		d.renameHint(ctx, b, m.From.ID, e, textRenameEmpty)
	case errors.Is(err, docsbot.ErrTitleTooLong):
		d.renameHint(ctx, b, m.From.ID, e, fmt.Sprintf(textRenameTooLong, docsbot.MaxTitleRunes))
	case errors.Is(err, docsbot.ErrNotFound):
		d.gone(ctx, b, e.chat, e.msg)
	case err != nil:
		d.fail(ctx, b, e.chat, "rename", err)
	default:
		d.log.InfoContext(ctx, "document renamed", "user_id", user, "doc_id", e.id)
		orig := d.origins.get(e.chat, e.msg)
		d.showCard(ctx, b, user, e.chat, e.msg, card.Doc, card.CategoryFolderID, orig)
	}
}

// renameHint re-arms the rename and tells, in the prompt message itself, what was wrong.
func (d *docs) renameHint(ctx context.Context, b *bot.Bot, tgUser int64, e renameEntry, hint string) {
	d.renames.set(tgUser, e.id, e.chat, e.msg)
	_ = d.out(b).edit(ctx, e.chat, e.msg, hint, withHTML(), withKeyboard(renameCancelKeyboard(e.id)))
}

// onDeleteAsk asks to confirm before deleting.
func (d *docs) onDeleteAsk(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnDeleteYes, CallbackData: cb(actDeleteYes, id)},
		{Text: btnDeleteNo, CallbackData: cb(actDeleteNo, id)},
	}}}
	_ = d.out(b).edit(ctx, chatID, msgID, d.deleteConfirmText(ctx, user, id), withKeyboard(kb))
}

// deleteConfirmText warns that the Drive file goes to the trash when the document
// is in a connected Drive. Any lookup failure falls back to the plain text.
func (d *docs) deleteConfirmText(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) string {
	card, err := d.svc.Card(ctx, user, id)
	if err != nil || !card.Doc.InDrive() {
		return textDeleteConfirm
	}
	if connected, err := d.svc.DriveConnected(ctx, user); err != nil || !connected {
		return textDeleteConfirm
	}
	return textDeleteConfirmDrive
}

func (d *docs) onDeleteYes(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	deleted, err := d.svc.Delete(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "delete", err)
		return
	}
	d.log.InfoContext(ctx, "document deleted", "user_id", user, "doc_id", id, "remote_kept", deleted.RemoteKept)
	if deleted.RemoteKept {
		// The Drive file stays: say so on the "deleted" note instead of returning to the list.
		_ = d.out(b).edit(ctx, chatID, msgID, textDeletedRemoteKept, withKeyboard(myDocsKeyboard()))
		return
	}
	d.returnToOrigin(ctx, b, u, user, chatID, msgID)
}

// returnToOrigin replaces the deleted card's message with the list or search it
// was opened from, falling back to a plain "deleted" note.
func (d *docs) returnToOrigin(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, chatID int64, msgID int) {
	orig := d.origins.get(chatID, msgID)
	switch {
	case strings.HasPrefix(orig, "l:"):
		parts := strings.Split(orig, ":")
		cat, ok := docsbot.ParseCategory(parts[1])
		if !ok || len(parts) < 3 {
			d.editDeleted(ctx, b, chatID, msgID)
			return
		}
		page, _ := strconv.Atoi(parts[2])
		p, err := d.svc.CategoryPage(ctx, user, cat, page)
		if err != nil {
			d.fail(ctx, b, chatID, "category page", err)
			return
		}
		if p.Total > 0 {
			text, kb := renderCategoryPage(p)
			_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
			return
		}
		d.editCatalog(ctx, b, user, chatID, msgID)
	case orig == "s":
		q, ok := d.searches.take(u.CallbackQuery.From.ID)
		if !ok {
			d.editCatalog(ctx, b, user, chatID, msgID)
			return
		}
		found, err := d.svc.Search(ctx, user, q)
		if err != nil {
			d.fail(ctx, b, chatID, "search", err)
			return
		}
		text, kb := renderFound(found)
		_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
	default:
		d.editDeleted(ctx, b, chatID, msgID)
	}
}

// editDeleted shows the "deleted" note with a way back to the catalog.
func (d *docs) editDeleted(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	_ = d.out(b).edit(ctx, chatID, msgID, textDeleted, withKeyboard(myDocsKeyboard()))
}

// onDeleteNo puts the card back in place of the confirmation.
func (d *docs) onDeleteNo(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	card, err := d.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	orig := d.origins.get(chatID, msgID)
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}

// sendFile resends a stored file with the method matching how it arrived.
// It reports whether the file went out.
func (d *docs) sendFile(ctx context.Context, b *bot.Bot, chatID int64, doc docsbot.Document) bool {
	caption := doc.Title + " · " + doc.Category.Label()
	op := "sendDocument"
	start := time.Now()
	err := d.retryOnRateLimit(ctx, func() error {
		var err error
		switch doc.Kind {
		case docsbot.KindPhoto:
			op = "sendPhoto"
			_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: doc.TgFileID}, Caption: caption})
		default:
			_, err = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: chatID, Document: &models.InputFileString{Data: doc.TgFileID}, Caption: caption})
		}
		return err
	})
	traceAPI(ctx, d.log, op, start)
	if err != nil {
		d.log.ErrorContext(ctx, "send file", "err", err, "user_id", doc.UserID, "doc_id", doc.ID)
		_, _ = d.out(b).send(ctx, chatID, textSendFailed)
		return false
	}
	d.log.InfoContext(ctx, "document sent", "user_id", doc.UserID, "doc_id", doc.ID)
	return true
}

func (d *docs) sendCard(ctx context.Context, b *bot.Bot, chatID int64, doc docsbot.Document, header, folderID string) {
	text := cardText(doc)
	if header != "" {
		text = header + "\n\n" + text
	}
	_, _ = d.out(b).send(ctx, chatID, text, withHTML(), withKeyboard(d.cardKB(ctx, doc, folderID, "")))
}

// editCard replaces a message with a document's card and remembers where the
// card was opened from (orig) so "Назад" and returns survive later edits.
func (d *docs) editCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, doc docsbot.Document, folderID, orig string) {
	d.editCardText(ctx, b, chatID, msgID, cardText(doc), d.cardKB(ctx, doc, folderID, orig), orig)
}

// editCardText is editCard with a prepared HTML text (the card plus a note).
func (d *docs) editCardText(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup, orig string) {
	_ = d.out(b).edit(ctx, chatID, msgID, text, withHTML(), withKeyboard(kb))
	d.origins.set(chatID, msgID, orig)
}

// cardKB is the card keyboard; a card whose sync failed on revoked access gets a
// reconnect link in place of "Повторить".
func (d *docs) cardKB(ctx context.Context, doc docsbot.Document, folderID, orig string) *models.InlineKeyboardMarkup {
	if !revoked(doc) {
		return cardKeyboard(doc, folderID, orig)
	}
	url, _ := d.connectURL(ctx, doc.UserID)
	return cardKeyboardWith(doc, folderID, orig, url)
}

// gone replaces a message about a vanished document with a way back to the catalog.
func (d *docs) gone(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	_ = d.out(b).edit(ctx, chatID, msgID, textGone, withKeyboard(myDocsKeyboard()))
}

// onOpenCard renders a document's card in place of the list/search message, with
// a "Назад" button back to where it was opened from.
func (d *docs) onOpenCard(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	card, err := d.svc.OpenCard(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil && card.Doc.ID == "" {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	if err != nil {
		// The Drive check failed on our side; the card read so far is still right to show.
		d.log.ErrorContext(ctx, "open card", "err", err, "user_id", user)
	}
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, strings.Join(parts[3:], ":"))
}
