package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// start greets the user. The OAuth redirect sends the user back with
// ?start=connected, which just confirms the connection. The button over inline
// results sends ?start=inline: with inline mode off, the user lands on the
// settings screen to turn it on; with it on, the user is told how to use it.
func (d *docs) start(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID := u.Message.Chat.ID
	payload := startPayload(u.Message.Text)
	if user, ok := userID(ctx); ok {
		d.svc.Start(ctx, user, sourcePayload(payload))
	}
	switch payload {
	case startConnected:
		d.out(b).Send(ctx, chatID, textDriveConnected)
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
			if d.botUsername != "" {
				d.out(b).Send(ctx, chatID, fmt.Sprintf(textStartInline, d.botUsername))
				return
			}
		}
	}
	d.out(b).Send(ctx, chatID, fmt.Sprintf(textStart, d.driveFolder))
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
	text, kb := renderSettings(view, url, haveURL, d.screenInfo())
	if id := d.out(b).Send(ctx, chatID, text, tg.WithHTML(), tg.WithKeyboard(kb)); id != 0 {
		d.settingsMsgs.Set(chatID, id)
	}
}

func (d *docs) help(ctx context.Context, b *bot.Bot, u *models.Update) {
	d.out(b).Send(ctx, u.Message.Chat.ID, d.helpText())
}

// helpText is /help; the inline-mode line is there only when the bot username is known.
func (d *docs) helpText() string {
	text := textHelp
	if d.botUsername != "" {
		text += fmt.Sprintf(textHelpInline, d.botUsername)
	}
	text += textHelpTail
	if d.privacyURL != "" {
		text += "\n\n" + fmt.Sprintf(textPrivacy, d.privacyURL)
	}
	return text
}

// screenInfo is the deployment-specific part of the settings texts.
func (d *docs) screenInfo() screenInfo {
	return screenInfo{driveFolder: d.driveFolder, botUsername: d.botUsername, privacyURL: d.privacyURL}
}

// onDisconnectAsk turns the settings message into the confirmation of a disconnect.
func (d *docs) onDisconnectAsk(ctx context.Context, b *bot.Bot, p press) {
	if !p.hasMsg {
		return
	}
	d.out(b).Edit(ctx, p.chatID, p.msgID, textDisconnectConfirm, tg.WithKeyboard(disconnectConfirmKeyboard()))
}

// onDisconnectNo puts the settings screen back in place of the confirmation.
func (d *docs) onDisconnectNo(ctx context.Context, b *bot.Bot, p press) {
	if !p.hasMsg {
		return
	}
	d.editSettings(ctx, b, p.user, p.chatID, p.msgID)
}

// editSettings redraws a message as the /settings screen and remembers it for the OAuth return.
func (d *docs) editSettings(ctx context.Context, b *bot.Bot, user docsbot.UserID, chatID int64, msgID int) {
	view, err := d.svc.Settings(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "settings", err)
		return
	}
	url, haveURL := d.connectURL(ctx, user)
	text, kb := renderSettings(view, url, haveURL, d.screenInfo())
	d.out(b).Edit(ctx, chatID, msgID, text, tg.WithHTML(), tg.WithKeyboard(kb))
	d.settingsMsgs.Set(chatID, msgID)
}

// onDisconnect turns Drive off and stops pending uploads, leaving stored
// documents (and any Drive links) untouched. A failure is shown to the user:
// the integration is then still connected.
func (d *docs) onDisconnect(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	if err := d.svc.Disconnect(ctx, user); err != nil {
		d.fail(ctx, b, chatID, "disconnect", err)
		return
	}
	d.log.InfoContext(ctx, "drive disconnected", "user_id", user)
	forgetSettings(d.settingsMsgs, chatID, msgID) // the message is no longer the settings screen
	d.out(b).Edit(ctx, chatID, msgID, textDisconnected, tg.WithKeyboard(nil))
}

// onDeleteData arms the "type УДАЛИТЬ" confirmation and explains what is removed.
func (d *docs) onDeleteData(ctx context.Context, b *bot.Bot, p press) {
	if !p.hasMsg {
		return
	}
	chatID := p.chatID
	if p.hasUser {
		d.log.InfoContext(ctx, "delete data requested", "user_id", p.user)
	}
	d.confirms.Set(p.tgUser, struct{}{})
	d.out(b).Send(ctx, chatID, fmt.Sprintf(textDeleteDataPrompt, confirmWord), tg.WithKeyboard(deleteDataKeyboard()))
}

// onDeleteDataNo drops the armed confirmation and turns its prompt into the cancel
// note. A late press (the data is already deleted, or the confirmation expired)
// only removes the buttons: the note would promise data that may be gone.
func (d *docs) onDeleteDataNo(ctx context.Context, b *bot.Bot, p press) {
	_, armed := d.confirms.Take(p.tgUser)
	if !p.hasMsg {
		return
	}
	if !armed {
		d.out(b).ClearKeyboard(ctx, p.chatID, p.msgID)
		return
	}
	d.out(b).Edit(ctx, p.chatID, p.msgID, textDeleteWrongWord, tg.WithKeyboard(nil))
}

// onDeleteConfirm deletes everything once the user typed the exact word.
func (d *docs) onDeleteConfirm(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, text string) {
	if !isConfirmWord(text) {
		d.out(b).Send(ctx, chatID, textDeleteWrongWord)
		return
	}
	if err := d.svc.DeleteAllData(ctx, user); errors.Is(err, docsbot.ErrRevokeIncomplete) {
		d.log.WarnContext(ctx, "drive revoke incomplete", "err", err, "user_id", user)
	} else if err != nil {
		d.fail(ctx, b, chatID, "delete all data", err)
		return
	}
	d.log.InfoContext(ctx, "data deleted", "user_id", user)
	d.settingsMsgs.Delete(chatID) // nothing left to refresh after a connect
	d.out(b).Send(ctx, chatID, textDataDeleted)
}

// onTransfer moves every not-yet-backed-up document to Drive; the offer message
// turns into a transfer status message.
func (d *docs) onTransfer(ctx context.Context, b *bot.Bot, p press) {
	user := p.user
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	view, ok, err := d.svc.TransferSaved(ctx, user, chatID, msgID)
	if err != nil {
		d.fail(ctx, b, chatID, "transfer", err)
		return
	}
	if !ok {
		d.out(b).Edit(ctx, chatID, msgID, textNothingToTransfer, tg.WithKeyboard(nil))
		return
	}
	d.log.InfoContext(ctx, "transfer started", "user_id", user, "count", len(view.Lines))
	d.redrawStatus(ctx, b, user, view)
}

func (d *docs) onTransferNo(ctx context.Context, b *bot.Bot, p press) {
	if !p.hasMsg {
		return
	}
	chatID, msgID := p.chatID, p.msgID
	d.out(b).Edit(ctx, chatID, msgID, textTransferDeclined, tg.WithKeyboard(nil))
}

// offerDrive nudges the user to connect Drive; the button opens the screen that
// explains what Google will ask.
func (d *docs) offerDrive(ctx context.Context, b *bot.Bot, chatID int64) {
	d.out(b).Send(ctx, chatID, textOfferDrive, tg.WithKeyboard(connectAskKeyboard()))
}

// onConnectAsk sends the screen before Google's consent page with the link to it,
// or just confirms the connection when Drive is already linked.
func (d *docs) onConnectAsk(ctx context.Context, b *bot.Bot, p press) {
	// A stale button of an old offer: Drive is already linked, nothing to explain.
	connected, err := d.svc.DriveConnected(ctx, p.user)
	if err != nil {
		d.fail(ctx, b, p.chatID, "drive status", err)
		return
	}
	if connected {
		d.out(b).Send(ctx, p.chatID, textDriveConnected)
		return
	}
	url, ok := d.connectURL(ctx, p.user)
	if !ok {
		d.out(b).Send(ctx, p.chatID, textError)
		return
	}
	d.out(b).Send(ctx, p.chatID, fmt.Sprintf(textConnectIntro, d.driveFolder), tg.WithKeyboard(connectIntroKeyboard(url)))
}

// onConnectNo turns the screen before Google's consent page into a note that the
// connection can wait.
func (d *docs) onConnectNo(ctx context.Context, b *bot.Bot, p press) {
	if !p.hasMsg {
		return
	}
	d.out(b).Edit(ctx, p.chatID, p.msgID, textConnectLater, tg.WithKeyboard(nil))
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

// sourcePayload is the /start argument as an acquisition source: the two payloads
// the bot sends itself are navigation, not a campaign, so they carry no source.
func sourcePayload(payload string) string {
	if payload == startConnected || payload == startInline {
		return ""
	}
	return payload
}

// confirmWord is the word the user must type to delete all their data; the prompt
// shows it, so the text and the check share this one value.
const confirmWord = "УДАЛИТЬ"

// isConfirmWord reports whether text is the confirmation word, in any letter case.
func isConfirmWord(text string) bool { return strings.EqualFold(strings.TrimSpace(text), confirmWord) }
