package telegram

import (
	"context"
	"errors"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// onRetryAll re-queues every failed Drive upload; the /settings message turns
// into a transfer status message.
func (d *docs) onRetryAll(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.settingsMsgs.forget(chatID, msgID) // the settings message turns into a status message
	view, ok, err := d.svc.RetryFailed(ctx, user, chatID, msgID)
	if err != nil {
		d.fail(ctx, b, chatID, "retry failed", err)
		return
	}
	if !ok {
		d.out(b).edit(ctx, chatID, msgID, textNoFailed, withKeyboard(nil))
		return
	}
	d.log.InfoContext(ctx, "retry all", "user_id", user, "count", len(view.Lines))
	d.redrawStatus(ctx, b, user, view)
}

// onRetry re-queues a failed backup. It answers the callback itself: a quiet
// toast when the document is no longer failed (double tap, stale button).
func (d *docs) onRetry(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	toast := ""
	defer func() { d.out(b).answer(ctx, u.CallbackQuery.ID, withToast(toast)) }()
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	err := d.svc.RetryBackup(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotRetryable) {
		toast = toastNotRetryable
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "retry backup", err)
		return
	}
	d.out(b).edit(ctx, chatID, msgID, textRetryQueued, withKeyboard(nil))
}

// onRetrySync re-queues a failed cloud sync and redraws the card as updating. It
// answers the callback itself, like onRetry.
func (d *docs) onRetrySync(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	toast := ""
	defer func() { d.out(b).answer(ctx, u.CallbackQuery.ID, withToast(toast)) }()
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	err := d.svc.RetrySync(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotRetryable) {
		toast = toastNotRetryable
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "retry sync", err)
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

// refreshStatus redraws the status message the document belongs to. Best-effort:
// failures are logged, not shown.
func (d *docs) refreshStatus(ctx context.Context, b *bot.Bot, user docsbot.UserID, doc docsbot.DocumentID) {
	view, err := d.svc.StatusView(ctx, user, doc)
	if err != nil {
		d.log.ErrorContext(ctx, "load status view", "err", err, "user_id", user, "doc_id", doc)
		return
	}
	d.redrawStatus(ctx, b, user, view)
}

func (d *docs) redrawStatus(ctx context.Context, b *bot.Bot, user docsbot.UserID, view docsbot.StatusView) {
	if !view.Grouped {
		return
	}
	text, kb := renderStatusMessage(view.Message, view.Lines, view.FolderByCat)
	// A redraw replaces a rename prompt shown on this message; the armed rename
	// must not outlive it. Dropped on both sides of the edit: a prompt armed while
	// it is in flight is overwritten by it too.
	d.renames.dropMessage(view.Message.ChatID, view.Message.MessageID)
	d.out(b).edit(ctx, view.Message.ChatID, view.Message.MessageID, text, withHTML(), withKeyboard(kb))
	d.renames.dropMessage(view.Message.ChatID, view.Message.MessageID)
	if view.AllTerminal {
		if err := d.svc.CloseStatus(ctx, user, view.Message.ID); err != nil {
			d.log.ErrorContext(ctx, "close status message", "err", err, "user_id", user)
		}
	}
}

// onRetryBatch re-queues the failed uploads of a status message and redraws it.
func (d *docs) onRetryBatch(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, _, ok := callbackMessage(u)
	if !ok {
		return
	}
	smID := docsbot.StatusMessageID(id)
	if _, err := d.svc.RetryStatusFailed(ctx, user, smID); err != nil {
		d.fail(ctx, b, chatID, "retry status failed", err)
		return
	}
	view, err := d.svc.StatusViewByID(ctx, user, smID)
	if err != nil {
		d.log.ErrorContext(ctx, "load status view", "err", err, "user_id", user)
		return
	}
	d.redrawStatus(ctx, b, user, view)
}
