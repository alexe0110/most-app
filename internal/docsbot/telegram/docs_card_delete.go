package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// onDeleteAsk asks to confirm before deleting.
func (d *docs) onDeleteAsk(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	id := p.data.docID
	if id == "" {
		return
	}
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	card, ok := d.loadCard(ctx, b, p, id)
	if !ok {
		return
	}
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnDeleteYes, CallbackData: docCB(actDeleteYes, id)},
		{Text: btnCancel, CallbackData: docCB(actDeleteNo, id)},
	}}}
	d.out(b).Edit(ctx, chatID, msgID, d.deleteConfirmText(ctx, user, card.Doc), tg.WithKeyboard(kb))
}

// deleteConfirmText names the document and, when it is in a connected Drive,
// warns that the Drive file goes to the trash. A failed Drive status falls back
// to the plain text.
func (d *docs) deleteConfirmText(ctx context.Context, user docsbot.UserID, doc docsbot.Document) string {
	if !doc.InDrive() {
		return fmt.Sprintf(textDeleteConfirm, doc.Title)
	}
	if connected, err := d.svc.DriveConnected(ctx, user); err != nil || !connected {
		return fmt.Sprintf(textDeleteConfirm, doc.Title)
	}
	return fmt.Sprintf(textDeleteConfirmDrive, doc.Title)
}

func (d *docs) onDeleteYes(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	id := p.data.docID
	if id == "" {
		return
	}
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	deleted, err := d.svc.Delete(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "delete", err)
		return
	}
	d.log.InfoContext(ctx, "document deleted", "user_id", user, "doc_id", id, "remote_kept", deleted.RemoteKept)
	// The message stops being a card; its origin is read once more below to return there.
	defer d.origins.Delete(originKey{chatID, msgID})
	if deleted.RemoteKept {
		// The Drive file stays: say so on the "deleted" note instead of returning to the list.
		d.out(b).Edit(ctx, chatID, msgID, textDeletedRemoteKept, tg.WithKeyboard(myDocsKeyboard()))
		return
	}
	d.returnToOrigin(ctx, b, p)
}

// returnToOrigin replaces the deleted card's message with the list or search it
// was opened from, falling back to a plain "deleted" note.
func (d *docs) returnToOrigin(ctx context.Context, b *bot.Bot, p press) {
	user, chatID, msgID := p.user, p.chatID, p.msgID
	orig := d.origin(chatID, msgID)
	switch {
	case strings.HasPrefix(orig, "l:"):
		parts := strings.Split(orig, ":")
		cat, ok := docsbot.ParseCategory(parts[1])
		if !ok || len(parts) < 3 {
			d.editDeleted(ctx, b, chatID, msgID)
			return
		}
		page, _ := strconv.Atoi(parts[2])
		cp, err := d.svc.CategoryPage(ctx, user, cat, page)
		if err != nil {
			d.fail(ctx, b, chatID, "category page", err)
			return
		}
		if cp.Total > 0 {
			text, kb := renderCategoryPage(cp)
			d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
			return
		}
		d.editCatalog(ctx, b, user, chatID, msgID)
	case orig == "s":
		q, ok := d.searches.Get(p.tgUser)
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
		d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
	default:
		d.editDeleted(ctx, b, chatID, msgID)
	}
}

// editDeleted shows the "deleted" note with a way back to the catalog.
func (d *docs) editDeleted(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	d.out(b).Edit(ctx, chatID, msgID, textDeleted, tg.WithKeyboard(myDocsKeyboard()))
}

// onDeleteNo puts the card back in place of the confirmation.
func (d *docs) onDeleteNo(ctx context.Context, b *bot.Bot, p press) {
	id := p.data.docID
	if id == "" {
		return
	}
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	card, ok := d.loadCard(ctx, b, p, id)
	if !ok {
		return
	}
	orig := d.origin(chatID, msgID)
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}
