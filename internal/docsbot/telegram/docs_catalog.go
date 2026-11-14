package telegram

import (
	"context"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
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
	d.out(b).Send(ctx, u.Message.Chat.ID, text, tg.WithKeyboard(kb))
}

// runSearch answers a typed query. It records the search here, not in Service.Search:
// that is also used to redraw the results on "back", and only this entry point
// knows the user asked a new question.
func (d *docs) runSearch(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, query string) {
	found, err := d.svc.Search(ctx, user, query)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	if !found.BlankQuery {
		d.svc.Track(ctx, user, docsbot.EventSearch, docsbot.EventProps{})
	}
	text, kb := renderFound(found)
	d.out(b).Send(ctx, chatID, text, tg.WithKeyboard(kb))
}

// onInlineSwitch applies the wanted inline-mode state from d:inl:<1|0> and
// redraws the settings message in place.
func (d *docs) onInlineSwitch(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if !p.hasMsg || len(p.data.args) < 1 || (p.data.args[0] != "1" && p.data.args[0] != "0") {
		return // the callback is already answered; a malformed payload changes nothing
	}
	chatID, msgID := p.chatID, p.msgID
	on := p.data.args[0] == "1"
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
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithHTML(), tg.WithKeyboard(kb))
	d.settingsMsgs.Set(chatID, msgID)
}

// myDocsKeyboard is a single "Мои документы" button back to the catalog.
func myDocsKeyboard() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnMyDocs, CallbackData: docCB(actListBack, "")},
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
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
}

// onListCat opens (or pages) a category, editing the list message in place.
func (d *docs) onListCat(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if len(p.data.args) < 2 {
		return
	}
	cat, ok := docsbot.ParseCategory(p.data.args[0])
	if !ok {
		return
	}
	page, _ := strconv.Atoi(p.data.args[1])
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	cp, err := d.svc.CategoryPage(ctx, user, cat, page)
	if err != nil {
		d.fail(ctx, b, chatID, "category page", err)
		return
	}
	text, kb := renderCategoryPage(cp)
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
}

func (d *docs) onListBack(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	cat, err := d.svc.Catalog(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "catalog", err)
		return
	}
	text, kb := renderCatalog(cat)
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
}

// onSearchBack re-runs the user's last search in place of the card; without a
// remembered query it falls back to the catalog.
func (d *docs) onSearchBack(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	q, ok := d.searches.Get(p.tgUser)
	if !ok {
		cat, err := d.svc.Catalog(ctx, user)
		if err != nil {
			d.fail(ctx, b, chatID, "catalog", err)
			return
		}
		text, kb := renderCatalog(cat)
		d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
		return
	}
	found, err := d.svc.Search(ctx, user, q)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	text, kb := renderFound(found)
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithKeyboard(kb))
}
