package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// start greets the user. The OAuth redirect sends the user back with
// ?start=connected, which just confirms the connection. The button over inline
// results sends ?start=inline: with inline mode off, the user lands on the
// settings screen to turn it on.
func (d *docs) start(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID := u.Message.Chat.ID
	switch startPayload(u.Message.Text) {
	case startConnected:
		reply(ctx, b, d.log, chatID, textDriveConnected)
		return
	case startInline:
		if user, ok := userID(ctx); ok {
			view, err := d.svc.Settings(ctx, user)
			if err != nil {
				d.fail(ctx, b, chatID, "settings", err)
				return
			}
			if !view.InlineEnabled {
				d.sendSettings(ctx, b, chatID, user, view)
				return
			}
		}
	}
	reply(ctx, b, d.log, chatID, fmt.Sprintf(textStart, d.driveFolder))
}

// settings shows the Drive connection state, document counts, and the data
// controls: connect/reconnect, disconnect, retry failed, export and delete.
func (d *docs) settings(ctx context.Context, b *bot.Bot, u *models.Update) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	chatID := u.Message.Chat.ID
	view, err := d.svc.Settings(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "settings", err)
		return
	}
	d.sendSettings(ctx, b, chatID, user, view)
}

// sendSettings sends the /settings screen for view.
func (d *docs) sendSettings(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, view docsbot.SettingsView) {
	url, haveURL := d.connectURL(ctx, user)
	text, kb := renderSettings(view, url, haveURL)
	if id, ok := d.sendWith(ctx, b, chatID, text, models.ParseModeHTML, kb); ok {
		d.settingsMsgs.set(chatID, id)
	}
}

func (d *docs) help(ctx context.Context, b *bot.Bot, u *models.Update) {
	reply(ctx, b, d.log, u.Message.Chat.ID, textHelp)
}

// onDisconnect turns Drive off and stops pending uploads, leaving stored
// documents (and any Drive links) untouched. A failure is shown to the user:
// the integration is then still connected.
func (d *docs) onDisconnect(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	if err := d.svc.Disconnect(ctx, user); err != nil {
		d.fail(ctx, b, chatID, "disconnect", err)
		return
	}
	d.log.InfoContext(ctx, "drive disconnected", "user_id", user)
	d.settingsMsgs.forget(chatID, msgID) // the message is no longer the settings screen
	d.editText(ctx, b, chatID, msgID, textDisconnected)
}

// onDeleteData arms the "type УДАЛИТЬ" confirmation and explains what is removed.
func (d *docs) onDeleteData(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID, ok := callbackChat(u)
	if !ok {
		return
	}
	if user, ok := userID(ctx); ok {
		d.log.InfoContext(ctx, "delete data requested", "user_id", user)
	}
	d.confirms.arm(u.CallbackQuery.From.ID)
	d.send(ctx, b, chatID, textDeleteDataPrompt, exportFirstKeyboard())
}

// onDeleteConfirm deletes everything once the user typed the exact word.
func (d *docs) onDeleteConfirm(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, text string) {
	if strings.TrimSpace(text) != confirmWord {
		reply(ctx, b, d.log, chatID, textDeleteWrongWord)
		return
	}
	if err := d.svc.DeleteAllData(ctx, user); errors.Is(err, docsbot.ErrRevokeIncomplete) {
		d.log.WarnContext(ctx, "drive revoke incomplete", "err", err, "user_id", user)
	} else if err != nil {
		d.fail(ctx, b, chatID, "delete all data", err)
		return
	}
	d.log.InfoContext(ctx, "data deleted", "user_id", user)
	d.settingsMsgs.forgetChat(chatID) // nothing left to refresh after a connect
	reply(ctx, b, d.log, chatID, textDataDeleted)
}

// onTransfer moves every not-yet-backed-up document to Drive; the offer message
// turns into a transfer status message.
func (d *docs) onTransfer(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	view, ok, err := d.svc.TransferSaved(ctx, user, chatID, msgID)
	if err != nil {
		d.fail(ctx, b, chatID, "transfer", err)
		return
	}
	if !ok {
		d.editText(ctx, b, chatID, msgID, textNothingToTransfer)
		return
	}
	d.log.InfoContext(ctx, "transfer started", "user_id", user, "count", len(view.Lines))
	d.redrawStatus(ctx, b, user, view)
}

func (d *docs) onTransferNo(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.editText(ctx, b, chatID, msgID, textTransferDeclined)
}

// offerDrive nudges the user to connect Drive, with a consent-link button.
func (d *docs) offerDrive(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID) {
	url, ok := d.connectURL(ctx, user)
	if !ok {
		return
	}
	d.send(ctx, b, chatID, textOfferDrive, connectKeyboard(url))
}

// connectURL builds the Google consent URL for the user, false if unavailable.
func (d *docs) connectURL(ctx context.Context, user docsbot.UserID) (string, bool) {
	if user == "" {
		return "", false
	}
	url, err := d.auth.AuthURL(ctx, string(user), docsbot.BotName)
	if err != nil {
		d.log.ErrorContext(ctx, "build auth url", "err", err)
		return "", false
	}
	return url, true
}

// /start deep-link payloads.
const (
	startConnected = "connected" // the OAuth redirect back to the bot
	startInline    = "inline"    // the button over inline results
)

// startPayload returns the argument of /start (?start=<payload>), "" if none.
func startPayload(text string) string {
	_, arg, _ := strings.Cut(text, " ")
	return strings.TrimSpace(arg)
}

// confirmWord is the exact word the user must type to delete all their data.
const confirmWord = "УДАЛИТЬ"
