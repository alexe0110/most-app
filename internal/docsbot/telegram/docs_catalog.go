package telegram

import (
	"context"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// list shows the catalog of categories.
func (d *docs) list(ctx context.Context, b *bot.Bot, u *models.Update) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	cat, err := d.svc.Catalog(ctx, user)
	if err != nil {
		d.fail(ctx, b, u.Message.Chat.ID, "catalog", err)
		return
	}
	text, kb := renderCatalog(cat)
	_, _ = d.out(b).send(ctx, u.Message.Chat.ID, text, withKeyboard(kb))
}

func (d *docs) runSearch(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, query string) {
	found, err := d.svc.Search(ctx, user, query)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	text, kb := renderFound(found)
	_, _ = d.out(b).send(ctx, chatID, text, withKeyboard(kb))
}

// onInlineSwitch applies the wanted inline-mode state from d:inl:<1|0> and
// redraws the settings message in place.
func (d *docs) onInlineSwitch(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok || len(parts) < 3 || (parts[2] != "1" && parts[2] != "0") {
		return // the callback is already answered; a malformed payload changes nothing
	}
	on := parts[2] == "1"
	if err := d.svc.SetInlineEnabled(ctx, user, on); err != nil {
		d.fail(ctx, b, chatID, "set inline enabled", err)
		return
	}
	view, err := d.svc.Settings(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "settings", err)
		return
	}
	d.log.InfoContext(ctx, "inline mode switched", "user_id", user, "enabled", on)
	url, haveURL := d.connectURL(ctx, user)
	text, kb := renderSettings(view, url, haveURL)
	_ = d.out(b).edit(ctx, chatID, msgID, text, withHTML(), withKeyboard(kb))
	d.settingsMsgs.set(chatID, msgID)
}

// myDocsKeyboard is a single "Мои документы" button back to the catalog.
func myDocsKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnMyDocs, CallbackData: cbPrefix + actListBack},
	}}}
}

// editCatalog replaces a message with the category catalog.
func (d *docs) editCatalog(ctx context.Context, b *bot.Bot, user docsbot.UserID, chatID int64, msgID int) {
	cat, err := d.svc.Catalog(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "catalog", err)
		return
	}
	text, kb := renderCatalog(cat)
	_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
}

// onListCat opens (or pages) a category, editing the list message in place.
func (d *docs) onListCat(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	if len(parts) < 4 {
		return
	}
	cat, ok := docsbot.ParseCategory(parts[2])
	if !ok {
		return
	}
	page, _ := strconv.Atoi(parts[3])
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	p, err := d.svc.CategoryPage(ctx, user, cat, page)
	if err != nil {
		d.fail(ctx, b, chatID, "category page", err)
		return
	}
	text, kb := renderCategoryPage(p)
	_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
}

func (d *docs) onListBack(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	cat, err := d.svc.Catalog(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "catalog", err)
		return
	}
	text, kb := renderCatalog(cat)
	_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
}

// onSearchBack re-runs the user's last search in place of the card; without a
// remembered query it falls back to the catalog.
func (d *docs) onSearchBack(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	q, ok := d.searches.take(u.CallbackQuery.From.ID)
	if !ok {
		cat, err := d.svc.Catalog(ctx, user)
		if err != nil {
			d.fail(ctx, b, chatID, "catalog", err)
			return
		}
		text, kb := renderCatalog(cat)
		_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
		return
	}
	found, err := d.svc.Search(ctx, user, q)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	text, kb := renderFound(found)
	_ = d.out(b).edit(ctx, chatID, msgID, text, withKeyboard(kb))
}
