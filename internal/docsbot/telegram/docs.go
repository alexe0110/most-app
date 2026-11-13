package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// botName identifies this bot in the shared integrations/oauth tables.
const botName = "docs"

// defaultDriveFolder is the Drive root folder name shown in /start unless
// WithDriveFolder overrides it. Mirrors the gdrive default (the adapter does
// not import platform/gdrive).
const defaultDriveFolder = "Most Docs"

// docs is the inbound adapter for the core document flow: it turns updates into
// Service calls and renders the replies. State is the domain's; the only local
// state is which document a user is renaming (force-reply).
type docs struct {
	svc      *docsbot.Service
	auth     Authorizer
	log      *slog.Logger
	renames  *renameStore
	confirms *confirmStore
	searches *searchStore
	origins  *originStore
	// settingsMsgs remembers the /settings message per chat for the OAuth return.
	settingsMsgs *settingsStore
	albums       *albumBuffer
	// driveFolder is the Drive root folder name shown in /start.
	driveFolder string
	// registerAlbum remembers an album's images for the "one PDF" button and
	// returns its token; set by Register (the converter owns the PDF work).
	registerAlbum func(user docsbot.UserID, group string, images []albumImage) string
	// pauseFile and pauseAlbum space out export sends under the Bot API rate limit.
	pauseFile, pauseAlbum time.Duration
	// exports holds the users with an export in flight: one at a time per user.
	exportsMu sync.Mutex
	exports   map[docsbot.UserID]bool
}

func newDocs(svc *docsbot.Service, auth Authorizer, log *slog.Logger, opts ...Option) *docs {
	d := &docs{
		svc: svc, auth: auth, log: log,
		renames:      newRenameStore(time.Now),
		confirms:     newConfirmStore(time.Now),
		searches:     newSearchStore(time.Now),
		origins:      newOriginStore(time.Now),
		settingsMsgs: newSettingsStore(time.Now),
		albums:       newAlbumBuffer(),

		driveFolder: defaultDriveFolder,

		pauseFile: exportPause, pauseAlbum: exportPerFilePause,
		exports: map[docsbot.UserID]bool{},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// ownsCallback matches this feature's inline buttons.
func (d *docs) ownsCallback(u *models.Update) bool {
	return u.CallbackQuery != nil && strings.HasPrefix(u.CallbackQuery.Data, cbPrefix)
}

// ownsFile matches a message carrying a photo or a document.
func (d *docs) ownsFile(u *models.Update) bool {
	return u.Message != nil && (len(u.Message.Photo) > 0 || u.Message.Document != nil)
}

// ownsUnsupported matches messages with media the bot does not store (and
// would otherwise misread as a search query).
func (d *docs) ownsUnsupported(u *models.Update) bool {
	m := u.Message
	return m != nil && (m.Voice != nil || m.Video != nil || m.VideoNote != nil || m.Audio != nil ||
		m.Sticker != nil || m.Animation != nil || m.Contact != nil || m.Location != nil || m.Poll != nil)
}

// onUnsupported tells the user which media the bot takes.
func (d *docs) onUnsupported(ctx context.Context, b *bot.Bot, u *models.Update) {
	reply(ctx, b, d.log, u.Message.Chat.ID, textUnsupported)
}

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
	d.send(ctx, b, u.Message.Chat.ID, text, kb)
}

// receive stores an incoming file and asks for a category (or reports a dup).
func (d *docs) receive(ctx context.Context, b *bot.Bot, u *models.Update) {
	user, ok := userID(ctx)
	if !ok {
		return
	}
	in, ok := incomingFile(u.Message, user)
	if !ok {
		return
	}
	chatID := u.Message.Chat.ID
	rep, err := d.svc.Receive(ctx, in)
	if err != nil {
		d.fail(ctx, b, chatID, "receive document", err)
		return
	}
	if in.MediaGroupID != "" {
		d.logReceived(ctx, user, rep)
		d.collectAlbum(ctx, b, chatID, user, in.MediaGroupID, rep, u.Message.ID, in)
		return
	}
	switch r := rep.(type) {
	case docsbot.NeedCategory:
		d.logReceived(ctx, user, rep)
		d.send(ctx, b, chatID, textChooseCategory, pickerKeyboard(r.Doc))
	case docsbot.AlreadySaved:
		d.logReceived(ctx, user, rep)
		d.sendAlreadySaved(ctx, b, chatID, user, r.Doc.ID)
	default:
		d.log.InfoContext(ctx, "document refused", "user_id", user, "reason", fmt.Sprintf("%T", rep))
		if text, ok := rejectionText(rep); ok {
			reply(ctx, b, d.log, chatID, text)
		}
	}
}

// sendAlreadySaved shows the card of a document the user already has.
func (d *docs) sendAlreadySaved(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) {
	card, err := d.svc.Card(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	d.sendCard(ctx, b, chatID, card.Doc, textAlreadySaved, card.CategoryFolderID)
}

// sendReplaced shows the card of a document whose file the converter replaced.
func (d *docs) sendReplaced(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID) {
	card, err := d.svc.Card(ctx, user, id)
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
	}
	d.log.InfoContext(ctx, "original replaced", "user_id", user, "doc_id", id)
	d.sendCard(ctx, b, chatID, card.Doc, textReplacedDone, card.CategoryFolderID)
}

// logReceived logs an accepted file: ids and kind only, never the title.
func (d *docs) logReceived(ctx context.Context, user docsbot.UserID, rep docsbot.Reply) {
	switch r := rep.(type) {
	case docsbot.NeedCategory:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind)
	case docsbot.AlreadySaved:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind, "duplicate", true)
	}
}

// onText handles any non-command text: a pending rename, otherwise a search. A
// text sent after the rename expired (or the bot restarted) is just a search.
func (d *docs) onText(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	chatID := u.Message.Chat.ID
	if strings.HasPrefix(u.Message.Text, "/") {
		reply(ctx, b, d.log, chatID, textUnknown)
		return
	}
	user, ok := userID(ctx)
	if !ok {
		return
	}
	if d.confirms.take(u.Message.From.ID) {
		d.onDeleteConfirm(ctx, b, chatID, user, u.Message.Text)
		return
	}
	if e, ok := d.renames.take(u.Message.From.ID); ok {
		d.applyRename(ctx, b, user, e, u.Message)
		return
	}
	d.searches.set(u.Message.From.ID, u.Message.Text)
	d.runSearch(ctx, b, chatID, user, u.Message.Text)
}

func (d *docs) runSearch(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, query string) {
	found, err := d.svc.Search(ctx, user, query)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	text, kb := renderFound(found)
	d.send(ctx, b, chatID, text, kb)
}

// callback dispatches an inline-button press by its action.
func (d *docs) callback(ctx context.Context, b *bot.Bot, u *models.Update) {
	parts := strings.Split(u.CallbackQuery.Data, ":") // d:<action>[:docID[:arg]]
	if !answersItself(parts) {
		d.answerText(ctx, b, u.CallbackQuery.ID, toastFor(parts))
	}
	user, ok := userID(ctx)
	if !ok {
		if answersItself(parts) {
			d.answerText(ctx, b, u.CallbackQuery.ID, "") // never leave the button spinning
		}
		return
	}
	if len(parts) < 2 {
		return
	}
	if parts[1] != actRenameCancel {
		// Any other button abandons a rename prompt that sits on another message.
		d.releaseRename(ctx, b, user, u)
	}
	switch parts[1] {
	case actCategory:
		d.onCategory(ctx, b, u, user, parts)
	case actCategoryGroup:
		d.onCategoryGroup(ctx, b, u, user, parts)
	case actGroupPick:
		d.onGroupPick(ctx, b, u, parts)
	case actSetCategory:
		d.onSetCategory(ctx, b, u, user, parts)
	case actChangeCat:
		d.onChangeCat(ctx, b, u, parts)
	case actSend:
		d.onSend(ctx, b, u, user, parts)
	case actRename:
		d.onRenamePrompt(ctx, b, u, parts)
	case actRenameCancel:
		d.onRenameCancel(ctx, b, u, user, parts)
	case actDeleteAsk:
		d.onDeleteAsk(ctx, b, u, user, parts)
	case actDeleteYes:
		d.onDeleteYes(ctx, b, u, user, parts)
	case actDeleteNo:
		d.onDeleteNo(ctx, b, u, user, parts)
	case actListCat:
		d.onListCat(ctx, b, u, user, parts)
	case actOpenCard:
		d.onOpenCard(ctx, b, u, user, parts)
	case actSearchBack:
		d.onSearchBack(ctx, b, u, user)
	case actListBack:
		d.onListBack(ctx, b, u, user)
	case actTransfer, actTransferYes:
		d.onTransfer(ctx, b, u, user)
	case actTransferNo:
		d.onTransferNo(ctx, b, u)
	case actRetry:
		d.onRetry(ctx, b, u, user, parts)
	case actRetrySync:
		d.onRetrySync(ctx, b, u, user, parts)
	case actDisconnect:
		d.onDisconnect(ctx, b, u, user)
	case actRetryAll:
		d.onRetryAll(ctx, b, u, user)
	case actExport:
		d.onExport(ctx, b, u, user)
	case actDeleteData:
		d.onDeleteData(ctx, b, u)
	case actRetryBatch:
		d.onRetryBatch(ctx, b, u, user, parts)
	case actInline:
		d.onInlineSwitch(ctx, b, u, user, parts)
	case actNoop:
	}
}

// answersItself reports whether the action handler answers the callback query
// on its own (to pick the toast from the outcome).
func answersItself(parts []string) bool {
	return len(parts) >= 2 && (parts[1] == actRetry || parts[1] == actRetrySync)
}

// toastFor is the short pop-up shown for an action, "" for none.
func toastFor(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	switch parts[1] {
	case actSend:
		return toastSending
	case actSetCategory, actDeleteYes:
		return toastDone
	}
	return ""
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
	d.editHTML(ctx, b, chatID, msgID, text, kb)
	d.settingsMsgs.set(chatID, msgID)
}

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
		d.editText(ctx, b, chatID, msgID, textNoFailed)
		return
	}
	d.log.InfoContext(ctx, "retry all", "user_id", user, "count", len(view.Lines))
	d.redrawStatus(ctx, b, user, view)
}

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

// onRetry re-queues a failed backup. It answers the callback itself: a quiet
// toast when the document is no longer failed (double tap, stale button).
func (d *docs) onRetry(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	toast := ""
	defer func() { d.answerText(ctx, b, u.CallbackQuery.ID, toast) }()
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
	d.editText(ctx, b, chatID, msgID, textRetryQueued)
}

// onRetrySync re-queues a failed cloud sync and redraws the card as updating. It
// answers the callback itself, like onRetry.
func (d *docs) onRetrySync(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	toast := ""
	defer func() { d.answerText(ctx, b, u.CallbackQuery.ID, toast) }()
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
	orig, _ := d.origins.get(chatID, msgID)
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}

// onCategory saves a new document under the chosen category. The picker message
// becomes the card (Drive off) or a live upload card / batch status (Drive on).
func (d *docs) onCategory(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, cat, ok := docCategory(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	saved, err := d.svc.ChooseCategory(ctx, user, id, cat)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "choose category", err)
		return
	}
	if saved.Already {
		return // double tap: the first tap already saved and tracked it
	}
	d.renderSaved(ctx, b, chatID, msgID, user, saved)
}

// renderSaved turns the text message msgID into the saved document's card (Drive
// off) or a live upload card / batch status (Drive on), then offers Drive.
func (d *docs) renderSaved(ctx context.Context, b *bot.Bot, chatID int64, msgID int, user docsbot.UserID, saved docsbot.Saved) {
	d.log.InfoContext(ctx, "document saved", "user_id", user, "doc_id", saved.Doc.ID,
		"category", saved.Doc.Category, "backup_status", saved.Doc.BackupStatus)
	if saved.Doc.BackupStatus == docsbot.BackupPending {
		// Drive is connected: the upload is queued and tracked in a status message.
		d.trackUpload(ctx, b, chatID, user, saved.Doc, msgID)
		return
	}
	d.editCard(ctx, b, chatID, msgID, saved.Doc, "", "")
	if saved.OfferDrive {
		d.offerDrive(ctx, b, chatID, user)
	}
}

// onCategoryGroup saves a whole album under the chosen category. The picker
// message becomes the card (one file), a batch status (Drive on) or a summary.
func (d *docs) onCategoryGroup(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, cat, ok := docCategory(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	group, err := d.svc.ChooseCategoryGroup(ctx, user, id, cat)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "choose album category", err)
		return
	}
	if group.Already {
		return // double tap: the first tap already saved and tracked the album
	}
	d.log.InfoContext(ctx, "album saved", "user_id", user, "category", cat, "count", len(group.Docs))

	var tracked []docsbot.Document // queued for upload, or kept in Telegram for size
	tooLarge := 0
	for _, doc := range group.Docs {
		switch doc.BackupStatus {
		case docsbot.BackupPending:
			tracked = append(tracked, doc)
		case docsbot.BackupTooLarge:
			tooLarge++
		}
	}
	switch {
	case len(tracked) > 0:
		// Show too-large files as lines of the same status message.
		for _, doc := range group.Docs {
			if doc.BackupStatus == docsbot.BackupTooLarge {
				tracked = append(tracked, doc)
			}
		}
		d.trackGroupUpload(ctx, b, chatID, user, tracked, msgID)
	case len(group.Docs) == 1:
		d.editCard(ctx, b, chatID, msgID, group.Docs[0], "", "")
	default:
		text := fmt.Sprintf(textGroupSaved, len(group.Docs), plural(len(group.Docs), "файл", "файла", "файлов"), cat.Label())
		if tooLarge > 0 {
			text += "\n" + fmt.Sprintf(textGroupTooLarge, tooLarge)
		}
		d.editText(ctx, b, chatID, msgID, text)
	}
	if group.OfferDrive {
		d.offerDrive(ctx, b, chatID, user)
	}
}

// trackGroupUpload turns the album picker into one status message holding every
// document of the album.
func (d *docs) trackGroupUpload(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, docs []docsbot.Document, pickerMsgID int) {
	kind := docsbot.StatusSingle
	if len(docs) > 1 {
		kind = docsbot.StatusBatch
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{Kind: kind}, docs, nil)
	d.editHTML(ctx, b, chatID, pickerMsgID, text, kb)
	sm, err := d.svc.OpenStatus(ctx, user, chatID, pickerMsgID, docs[0].ID, kind)
	if err != nil {
		d.fail(ctx, b, chatID, "open status", err)
		return
	}
	for _, doc := range docs[1:] {
		if err := d.svc.AttachStatus(ctx, user, doc.ID, sm.ID); err != nil {
			d.fail(ctx, b, chatID, "attach status", err)
			return
		}
	}
}

// trackUpload shows a queued document's upload status. A lone document turns the
// picker message into a live card; further documents inside the grouping window
// share one batch message, and their pickers are retired or become that batch.
func (d *docs) trackUpload(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, doc docsbot.Document, pickerMsgID int) {
	sm, reuse, err := d.svc.StatusTarget(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "status target", err)
		return
	}
	kind := docsbot.StatusSingle
	switch {
	case reuse && sm.Kind == docsbot.StatusBatch:
		if err := d.svc.AttachStatus(ctx, user, doc.ID, sm.ID); err != nil {
			d.fail(ctx, b, chatID, "attach status", err)
			return
		}
		d.deleteMessage(ctx, b, chatID, pickerMsgID)
		d.refreshStatus(ctx, b, user, doc.ID)
		return
	case reuse && sm.Kind == docsbot.StatusSingle:
		// The earlier single card stays as is; a new batch starts from this picker.
		kind = docsbot.StatusBatch
	case reuse && sm.Kind == docsbot.StatusTransfer:
		// An open transfer is intentionally not grouped with: this picker becomes
		// its own single card (the default below).
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{Kind: kind}, []docsbot.Document{doc}, nil)
	d.editHTML(ctx, b, chatID, pickerMsgID, text, kb)
	if _, err := d.svc.OpenStatus(ctx, user, chatID, pickerMsgID, doc.ID, kind); err != nil {
		d.fail(ctx, b, chatID, "open status", err)
	}
}

// deleteMessage removes a message; failures are logged, not shown.
func (d *docs) deleteMessage(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	start := time.Now()
	_, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
	traceAPI(ctx, d.log, "deleteMessage", start)
	if err != nil {
		d.log.ErrorContext(ctx, "delete message", "err", err)
	}
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
	d.editHTML(ctx, b, view.Message.ChatID, view.Message.MessageID, text, kb)
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

// editHTML rewrites an HTML message (a card, a status message, a rename prompt);
// "message is not modified" is not an error.
func (d *docs) editHTML(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML}
	if kb == nil {
		// An omitted reply_markup keeps the old keyboard; an empty one clears it.
		kb = &models.InlineKeyboardMarkup{}
	}
	params.ReplyMarkup = kb
	start := time.Now()
	_, err := b.EditMessageText(ctx, params)
	traceAPI(ctx, d.log, "editMessageText", start)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		d.log.ErrorContext(ctx, "edit html message", "err", err)
	}
}

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
	orig, _ := d.origins.get(chatID, msgID)
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
	d.editMarkup(ctx, b, chatID, msgID, categoryKeyboard(actSetCategory, id))
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
		reply(ctx, b, d.log, chatID, textGone)
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
	d.editHTML(ctx, b, chatID, msgID, textRenamePrompt, renameCancelKeyboard(id))
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
	orig, _ := d.origins.get(chatID, msgID)
	d.showCard(ctx, b, user, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
}

// showCard puts a document's card on a message. A message that is the live status
// message of the document gets the status rendering instead, so its retry row and
// storage line stay; anything else gets the plain card.
func (d *docs) showCard(ctx context.Context, b *bot.Bot, user docsbot.UserID, chatID int64, msgID int, doc docsbot.Document, folderID, orig string) {
	if view, err := d.svc.StatusView(ctx, user, doc.ID); err == nil && view.Grouped &&
		view.Message.ChatID == chatID && view.Message.MessageID == msgID {
		text, kb := renderStatusMessage(view.Message, view.Lines, view.FolderByCat)
		d.editHTML(ctx, b, chatID, msgID, text, kb)
		return
	}
	d.editCard(ctx, b, chatID, msgID, doc, folderID, orig)
}

// applyRename takes the typed name: the user's message is removed (the chat stays
// clean), then the prompt message becomes the renamed card, or shows a hint and
// keeps waiting when the name is unusable.
func (d *docs) applyRename(ctx context.Context, b *bot.Bot, user docsbot.UserID, e renameEntry, m *models.Message) {
	d.deleteUserMessage(ctx, b, m.Chat.ID, m.ID)
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
		orig, _ := d.origins.get(e.chat, e.msg)
		d.showCard(ctx, b, user, e.chat, e.msg, card.Doc, card.CategoryFolderID, orig)
	}
}

// renameHint re-arms the rename and tells, in the prompt message itself, what was wrong.
func (d *docs) renameHint(ctx context.Context, b *bot.Bot, tgUser int64, e renameEntry, hint string) {
	d.renames.set(tgUser, e.id, e.chat, e.msg)
	d.editHTML(ctx, b, e.chat, e.msg, hint, renameCancelKeyboard(e.id))
}

// deleteUserMessage removes the user's message from the chat. It is cosmetic: a
// failure (too old, no right) is only logged.
func (d *docs) deleteUserMessage(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	start := time.Now()
	_, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: msgID})
	traceAPI(ctx, d.log, "deleteMessage", start)
	if err != nil {
		d.log.WarnContext(ctx, "delete user message", "err", err)
	}
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
	d.editMessage(ctx, b, chatID, msgID, d.deleteConfirmText(ctx, user, id), kb)
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
		d.editMessage(ctx, b, chatID, msgID, textDeletedRemoteKept, myDocsKeyboard())
		return
	}
	d.returnToOrigin(ctx, b, u, user, chatID, msgID)
}

// returnToOrigin replaces the deleted card's message with the list or search it
// was opened from, falling back to a plain "deleted" note.
func (d *docs) returnToOrigin(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, chatID int64, msgID int) {
	orig, _ := d.origins.get(chatID, msgID)
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
			d.editMessage(ctx, b, chatID, msgID, text, kb)
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
		d.editMessage(ctx, b, chatID, msgID, text, kb)
	default:
		d.editDeleted(ctx, b, chatID, msgID)
	}
}

// editDeleted shows the "deleted" note with a way back to the catalog.
func (d *docs) editDeleted(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	d.editMessage(ctx, b, chatID, msgID, textDeleted, myDocsKeyboard())
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
	d.editMessage(ctx, b, chatID, msgID, text, kb)
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
	orig, _ := d.origins.get(chatID, msgID)
	d.editCard(ctx, b, chatID, msgID, card.Doc, card.CategoryFolderID, orig)
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
	d.editMessage(ctx, b, chatID, msgID, text, kb)
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
	d.editMessage(ctx, b, chatID, msgID, text, kb)
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
	if d.auth == nil || user == "" {
		return "", false
	}
	url, err := d.auth.AuthURL(ctx, string(user), botName)
	if err != nil {
		d.log.Error("build auth url", "err", err)
		return "", false
	}
	return url, true
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
		reply(ctx, b, d.log, chatID, textSendFailed)
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
	d.sendHTML(ctx, b, chatID, text, d.cardKB(ctx, doc, folderID, ""))
}

// editCard replaces a message with a document's card and remembers where the
// card was opened from (orig) so "Назад" and returns survive later edits.
func (d *docs) editCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, doc docsbot.Document, folderID, orig string) {
	d.editCardText(ctx, b, chatID, msgID, cardText(doc), d.cardKB(ctx, doc, folderID, orig), orig)
}

// editCardText is editCard with a prepared HTML text (the card plus a note).
func (d *docs) editCardText(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup, orig string) {
	d.editHTML(ctx, b, chatID, msgID, text, kb)
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
	d.editMessage(ctx, b, chatID, msgID, textGone, myDocsKeyboard())
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
		d.editMessage(ctx, b, chatID, msgID, text, kb)
		return
	}
	found, err := d.svc.Search(ctx, user, q)
	if err != nil {
		d.fail(ctx, b, chatID, "search", err)
		return
	}
	text, kb := renderFound(found)
	d.editMessage(ctx, b, chatID, msgID, text, kb)
}

// sendHTML posts an HTML message, with an inline keyboard when kb is non-nil.
func (d *docs) sendHTML(ctx context.Context, b *bot.Bot, chatID int64, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	_, err := b.SendMessage(ctx, params)
	traceAPI(ctx, d.log, "sendMessage", start)
	if err != nil {
		d.log.ErrorContext(ctx, "send telegram message", "err", err)
	}
}

// send posts a message, with an inline keyboard when kb is non-nil.
func (d *docs) send(ctx context.Context, b *bot.Bot, chatID int64, text string, kb *models.InlineKeyboardMarkup) (int, bool) {
	return d.sendWith(ctx, b, chatID, text, "", kb)
}

// sendWith is send with a parse mode ("" for plain text). It returns the id of
// the sent message; ok is false when the send failed (logged here).
func (d *docs) sendWith(ctx context.Context, b *bot.Bot, chatID int64, text string, mode models.ParseMode, kb *models.InlineKeyboardMarkup) (int, bool) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: mode}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	msg, err := b.SendMessage(ctx, params)
	traceAPI(ctx, d.log, "sendMessage", start)
	if err != nil {
		d.log.ErrorContext(ctx, "send telegram message", "err", err)
		return 0, false
	}
	return msg.ID, true
}

// editMessage rewrites a message's text and keyboard in place (used for paging).
func (d *docs) editMessage(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text}
	if kb == nil {
		// An omitted reply_markup keeps the old keyboard; an empty one clears it.
		kb = &models.InlineKeyboardMarkup{}
	}
	params.ReplyMarkup = kb
	start := time.Now()
	_, err := b.EditMessageText(ctx, params)
	traceAPI(ctx, d.log, "editMessageText", start)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		d.log.Error("edit telegram message", "err", err)
	}
}

// editText rewrites a message's text and drops its keyboard (retiring buttons).
func (d *docs) editText(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string) {
	d.editMessage(ctx, b, chatID, msgID, text, nil)
}

// editMarkup replaces a message's inline keyboard, keeping its text.
func (d *docs) editMarkup(ctx context.Context, b *bot.Bot, chatID int64, msgID int, kb *models.InlineKeyboardMarkup) {
	start := time.Now()
	_, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID: chatID, MessageID: msgID, ReplyMarkup: kb,
	})
	traceAPI(ctx, d.log, "editMessageReplyMarkup", start)
	if err != nil {
		d.log.Error("edit telegram markup", "err", err)
	}
}

// answerText stops the button's loading spinner, showing text as a toast if set.
func (d *docs) answerText(ctx context.Context, b *bot.Bot, callbackID, text string) {
	start := time.Now()
	_, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text})
	traceAPI(ctx, d.log, "answerCallbackQuery", start)
	if err != nil {
		d.log.Error("answer callback", "err", err)
	}
}

// fail logs the real error once and shows the user a generic message.
func (d *docs) fail(ctx context.Context, b *bot.Bot, chatID int64, what string, err error) {
	if user, ok := userID(ctx); ok {
		d.log.ErrorContext(ctx, what, "err", err, "user_id", user)
	} else {
		d.log.ErrorContext(ctx, what, "err", err)
	}
	reply(ctx, b, d.log, chatID, textError)
}

// userID reads the internal user id the ensureUser middleware put in ctx.
func userID(ctx context.Context) (docsbot.UserID, bool) {
	id, ok := tg.UserIDFromContext(ctx)
	return docsbot.UserID(id), ok
}

// docID reads the document id from callback parts "d:<action>:<docID>".
func docID(parts []string) (docsbot.DocumentID, bool) {
	if len(parts) < 3 || parts[2] == "" {
		return "", false
	}
	return docsbot.DocumentID(parts[2]), true
}

// docCategory reads id and category from "d:<action>:<docID>:<slug>".
func docCategory(parts []string) (docsbot.DocumentID, docsbot.Category, bool) {
	if len(parts) < 4 {
		return "", "", false
	}
	cat, ok := docsbot.ParseCategory(parts[3])
	if !ok {
		return "", "", false
	}
	return docsbot.DocumentID(parts[2]), cat, true
}

// callbackMessage returns the chat and message id behind a callback button.
func callbackMessage(u *models.Update) (chatID int64, msgID int, ok bool) {
	m := u.CallbackQuery.Message.Message
	if m == nil {
		return 0, 0, false
	}
	return m.Chat.ID, m.ID, true
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

// renameStore remembers which document a user is renaming and on which message
// the prompt sits. In memory only: a restart drops it, and a stale arm (older
// than the TTL) is ignored.
type renameStore struct {
	mu  sync.Mutex
	m   map[int64]renameEntry
	now func() time.Time
}

type renameEntry struct {
	id   docsbot.DocumentID
	chat int64 // the card's message: it shows the prompt, then the renamed card
	msg  int
	at   time.Time
}

const renameTTL = 5 * time.Minute

func newRenameStore(now func() time.Time) *renameStore {
	return &renameStore{m: map[int64]renameEntry{}, now: now}
}

func (s *renameStore) set(user int64, id docsbot.DocumentID, chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = renameEntry{id: id, chat: chat, msg: msg, at: s.now()}
}

// take removes and returns the armed rename, if any and not expired.
func (s *renameStore) take(user int64) (renameEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.m[user]
	if !found {
		return renameEntry{}, false
	}
	delete(s.m, user)
	if s.now().Sub(e.at) > renameTTL {
		return renameEntry{}, false
	}
	return e, true
}

// dropMessage forgets every rename armed on that message, whoever armed it. A
// message that is redrawn (a status message) no longer shows the prompt.
func (s *renameStore) dropMessage(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for user, e := range s.m {
		if e.chat == chat && e.msg == msg {
			delete(s.m, user)
		}
	}
}

// Renames is the rename state shared by the docs handlers and the Notifier: the
// worker redraws status messages, and a redraw ends a rename prompt shown on one.
type Renames struct{ store *renameStore }

// NewRenames makes the shared rename state; pass it to Register (WithRenames) and
// to NewNotifier (WithNotifierRenames).
func NewRenames() *Renames { return &Renames{store: newRenameStore(time.Now)} }

// drop forgets the user's rename if it is armed on that message; one armed on
// another message (a newer prompt) is left alone.
func (s *renameStore) drop(user int64, chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[user]; ok && e.chat == chat && e.msg == msg {
		delete(s.m, user)
	}
}

// originStore remembers where each card message was opened from (a list page, the
// search, or nowhere) so edits of the card keep its "Назад" and delete can return.
// In memory only; entries expire after originTTL.
type originStore struct {
	mu  sync.Mutex
	m   map[originKey]originEntry
	now func() time.Time
}

type originKey struct {
	chat int64
	msg  int
}

type originEntry struct {
	orig string // "l:<cat>:<page>", "s" or ""
	at   time.Time
}

const originTTL = 30 * time.Minute

func newOriginStore(now func() time.Time) *originStore {
	return &originStore{m: map[originKey]originEntry{}, now: now}
}

func (s *originStore) set(chat int64, msg int, orig string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[originKey{chat, msg}] = originEntry{orig: orig, at: s.now()}
}

// get returns the origin if present and not expired; the entry is kept.
func (s *originStore) get(chat int64, msg int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := originKey{chat, msg}
	e, ok := s.m[k]
	if !ok {
		return "", false
	}
	if s.now().Sub(e.at) > originTTL {
		delete(s.m, k)
		return "", false
	}
	return e.orig, true
}

// searchStore remembers each user's last search query so a card opened from the
// results can go back to them. In memory only; entries expire after searchTTL.
type searchStore struct {
	mu  sync.Mutex
	m   map[int64]searchEntry
	now func() time.Time
}

type searchEntry struct {
	query string
	at    time.Time
}

const searchTTL = 10 * time.Minute

func newSearchStore(now func() time.Time) *searchStore {
	return &searchStore{m: map[int64]searchEntry{}, now: now}
}

func (s *searchStore) set(user int64, query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = searchEntry{query: query, at: s.now()}
}

// take returns the remembered query if not expired. It keeps the entry so
// repeated "Назад" presses keep working.
func (s *searchStore) take(user int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[user]
	if !ok {
		return "", false
	}
	if s.now().Sub(e.at) > searchTTL {
		delete(s.m, user)
		return "", false
	}
	return e.query, true
}

// confirmWord is the exact word the user must type to delete all their data.
const confirmWord = "УДАЛИТЬ"

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

// confirmStore remembers that a user is being asked to confirm data deletion. In
// memory only, like renameStore: a restart or a stale arm just means the user
// taps "Удалить мои данные" again.
type confirmStore struct {
	mu  sync.Mutex
	m   map[int64]time.Time
	now func() time.Time
}

const confirmTTL = 2 * time.Minute

func newConfirmStore(now func() time.Time) *confirmStore {
	return &confirmStore{m: map[int64]time.Time{}, now: now}
}

func (s *confirmStore) arm(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = s.now()
}

// take reports whether the user has a live deletion confirmation armed, clearing it.
func (s *confirmStore) take(user int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.m[user]
	if !ok {
		return false
	}
	delete(s.m, user)
	return s.now().Sub(at) <= confirmTTL
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
