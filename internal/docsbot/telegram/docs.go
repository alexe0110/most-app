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
}

func newDocs(svc *docsbot.Service, auth Authorizer, log *slog.Logger) *docs {
	return &docs{
		svc: svc, auth: auth, log: log,
		renames:  newRenameStore(time.Now),
		confirms: newConfirmStore(time.Now),
		searches: newSearchStore(time.Now),
		origins:  newOriginStore(time.Now),
	}
}

// ownsCallback matches this feature's inline buttons.
func (d *docs) ownsCallback(u *models.Update) bool {
	return u.CallbackQuery != nil && strings.HasPrefix(u.CallbackQuery.Data, cbPrefix)
}

// ownsFile matches a message carrying a photo or a document.
func (d *docs) ownsFile(u *models.Update) bool {
	return u.Message != nil && (len(u.Message.Photo) > 0 || u.Message.Document != nil)
}

// start greets the user. The OAuth redirect sends the user back with
// ?start=connected, which just confirms the connection.
func (d *docs) start(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID := u.Message.Chat.ID
	if deepLinkConnected(u.Message.Text) {
		reply(ctx, b, d.log, chatID, textDriveConnected)
		return
	}
	reply(ctx, b, d.log, chatID, textStart)
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
	url, haveURL := d.connectURL(ctx, user)
	text, kb := renderSettings(view, url, haveURL)
	d.send(ctx, b, chatID, text, kb)
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
	switch r := rep.(type) {
	case docsbot.NeedCategory:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind)
		d.send(ctx, b, chatID, textChooseCategory, categoryKeyboard(actCategory, r.Doc.ID))
	case docsbot.AlreadySaved:
		d.log.InfoContext(ctx, "document received", "user_id", user, "doc_id", r.Doc.ID, "kind", r.Doc.Kind, "duplicate", true)
		card, err := d.svc.Card(ctx, user, r.Doc.ID)
		if err != nil {
			d.fail(ctx, b, chatID, "load card", err)
			return
		}
		d.sendCard(ctx, b, chatID, card.Doc, textAlreadySaved, card.CategoryFolderID)
	}
}

// onText handles any non-command text: a pending rename, otherwise a search.
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
	if id, chat, msg, ok := d.renames.take(u.Message.From.ID); ok {
		d.applyRename(ctx, b, chatID, user, id, chat, msg, u.Message.Text)
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
	d.answerText(ctx, b, u.CallbackQuery.ID, toastFor(parts))
	user, ok := userID(ctx)
	if !ok {
		return
	}
	if len(parts) < 2 {
		return
	}
	switch parts[1] {
	case actCategory:
		d.onCategory(ctx, b, u, user, parts)
	case actSetCategory:
		d.onSetCategory(ctx, b, u, user, parts)
	case actChangeCat:
		d.onChangeCat(ctx, b, u, parts)
	case actSend:
		d.onSend(ctx, b, u, user, parts)
	case actRename:
		d.onRenamePrompt(ctx, b, u, parts)
	case actDeleteAsk:
		d.onDeleteAsk(ctx, b, u, parts)
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
	case actNoop:
	}
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

// onDisconnect revokes Drive access and stops pending uploads, leaving stored
// documents (and any Drive links) untouched.
func (d *docs) onDisconnect(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	if err := d.svc.Disconnect(ctx, user); err != nil {
		d.fail(ctx, b, chatID, "disconnect", err)
		return
	}
	d.editText(ctx, b, chatID, msgID, textDisconnected)
}

// onRetryAll re-queues every failed Drive upload.
func (d *docs) onRetryAll(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	n, err := d.svc.RetryFailed(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "retry failed", err)
		return
	}
	if n == 0 {
		d.editText(ctx, b, chatID, msgID, textNoFailed)
		return
	}
	d.editText(ctx, b, chatID, msgID, fmt.Sprintf(textRetryAllQueued, n))
}

// onExport resends the user's stored documents from Telegram, paced under the
// Bot API rate limit, up to the service's per-request cap.
func (d *docs) onExport(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	chatID, ok := callbackChat(u)
	if !ok {
		return
	}
	exp, err := d.svc.ExportAll(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "export", err)
		return
	}
	if exp.Total == 0 {
		reply(ctx, b, d.log, chatID, textExportEmpty)
		return
	}
	reply(ctx, b, d.log, chatID, textExportStart)
	for i, doc := range exp.Docs {
		if i > 0 && !pause(ctx, exportPause) {
			return // context cancelled; stop sending
		}
		d.sendFile(ctx, b, chatID, doc)
	}
	if exp.Truncated {
		reply(ctx, b, d.log, chatID, fmt.Sprintf(textExportTruncated, len(exp.Docs), exp.Total))
		return
	}
	reply(ctx, b, d.log, chatID, fmt.Sprintf(textExportDone, len(exp.Docs)))
}

// onDeleteData arms the "type УДАЛИТЬ" confirmation and explains what is removed.
func (d *docs) onDeleteData(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID, ok := callbackChat(u)
	if !ok {
		return
	}
	d.confirms.arm(u.CallbackQuery.From.ID)
	reply(ctx, b, d.log, chatID, textDeleteDataPrompt)
}

// onDeleteConfirm deletes everything once the user typed the exact word.
func (d *docs) onDeleteConfirm(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, text string) {
	if strings.TrimSpace(text) != confirmWord {
		reply(ctx, b, d.log, chatID, textDeleteWrongWord)
		return
	}
	if err := d.svc.DeleteAllData(ctx, user); err != nil {
		d.fail(ctx, b, chatID, "delete all data", err)
		return
	}
	reply(ctx, b, d.log, chatID, textDataDeleted)
}

// onTransfer queues every not-yet-backed-up document for upload to Drive.
func (d *docs) onTransfer(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID) {
	count, err := d.svc.TransferSaved(ctx, user)
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		if cid, found := callbackChat(u); found && err != nil {
			d.fail(ctx, b, cid, "transfer", err)
		}
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "transfer", err)
		return
	}
	d.editText(ctx, b, chatID, msgID, fmt.Sprintf(textTransferStarted, count))
}

func (d *docs) onTransferNo(ctx context.Context, b *bot.Bot, u *models.Update) {
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	d.editText(ctx, b, chatID, msgID, textTransferDeclined)
}

// onRetry re-queues a failed backup.
func (d *docs) onRetry(ctx context.Context, b *bot.Bot, u *models.Update, user docsbot.UserID, parts []string) {
	id, ok := docID(parts)
	if !ok {
		return
	}
	chatID, msgID, ok := callbackMessage(u)
	if !ok {
		return
	}
	if err := d.svc.RetryBackup(ctx, user, id); err != nil {
		d.fail(ctx, b, chatID, "retry backup", err)
		return
	}
	d.editText(ctx, b, chatID, msgID, textRetryQueued)
}

// onCategory saves a new document under the chosen category, retires the picker
// buttons (replacing them with a ✅), and shows the card.
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
	if err != nil {
		d.fail(ctx, b, chatID, "choose category", err)
		return
	}
	d.log.InfoContext(ctx, "document saved", "user_id", user, "doc_id", saved.Doc.ID,
		"category", cat, "backup_status", saved.Doc.BackupStatus)
	d.editText(ctx, b, chatID, msgID, "✅ "+categoryLabel(cat))
	if saved.Doc.BackupStatus == docsbot.BackupPending {
		// Drive is connected: the upload is queued and tracked in a batch status message.
		d.trackUpload(ctx, b, chatID, user, saved.Doc)
		return
	}
	d.sendCard(ctx, b, chatID, saved.Doc, textSaved, "")
	if saved.OfferDrive {
		d.offerDrive(ctx, b, chatID, user)
	}
}

// trackUpload adds a queued document to the user's open status message, or opens
// a new one when there is none inside the grouping window.
func (d *docs) trackUpload(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, doc docsbot.Document) {
	sm, reuse, err := d.svc.StatusTarget(ctx, user)
	if err != nil {
		d.fail(ctx, b, chatID, "status target", err)
		return
	}
	if reuse {
		if err := d.svc.AttachStatus(ctx, doc.ID, sm.ID); err != nil {
			d.fail(ctx, b, chatID, "attach status", err)
			return
		}
		d.refreshStatus(ctx, b, user, doc.ID)
		return
	}
	text, kb := renderStatusMessage(docsbot.StatusMessage{}, []docsbot.Document{doc}, nil)
	messageID, err := d.sendHTML(ctx, b, chatID, text, kb)
	if err != nil {
		d.fail(ctx, b, chatID, "send status message", err)
		return
	}
	if _, err := d.svc.OpenStatus(ctx, user, chatID, messageID, doc.ID); err != nil {
		d.fail(ctx, b, chatID, "open status", err)
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
	d.editHTML(ctx, b, view.Message.ChatID, view.Message.MessageID, text, kb)
	if view.AllTerminal {
		if err := d.svc.CloseStatus(ctx, view.Message.ID); err != nil {
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

// sendHTML posts an HTML message and returns its id.
func (d *docs) sendHTML(ctx context.Context, b *bot.Bot, chatID int64, text string, kb *models.InlineKeyboardMarkup) (int, error) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	msg, err := b.SendMessage(ctx, params)
	traceAPI(ctx, d.log, "sendMessage", start)
	if err != nil {
		return 0, fmt.Errorf("send html message: %w", err)
	}
	return msg.ID, nil
}

// editHTML rewrites an HTML message; "message is not modified" is not an error.
func (d *docs) editHTML(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	_, err := b.EditMessageText(ctx, params)
	traceAPI(ctx, d.log, "editMessageText", start)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		d.log.ErrorContext(ctx, "edit status message", "err", err)
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

// onRenamePrompt arms the rename and asks for the new name via force reply.
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
	start := time.Now()
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        textRenamePrompt,
		ReplyMarkup: &models.ForceReply{ForceReply: true, InputFieldPlaceholder: "Новое имя"},
	})
	traceAPI(ctx, d.log, "sendMessage", start)
	if err != nil {
		d.log.Error("send rename prompt", "err", err)
	}
}

func (d *docs) applyRename(ctx context.Context, b *bot.Bot, chatID int64, user docsbot.UserID, id docsbot.DocumentID, cardChat int64, cardMsg int, title string) {
	card, err := d.svc.Rename(ctx, user, id, title)
	switch {
	case errors.Is(err, docsbot.ErrEmptyTitle):
		reply(ctx, b, d.log, chatID, textRenameEmpty)
	case errors.Is(err, docsbot.ErrNotFound):
		reply(ctx, b, d.log, chatID, textGone)
	case err != nil:
		d.fail(ctx, b, chatID, "rename", err)
	default:
		d.log.InfoContext(ctx, "document renamed", "user_id", user, "doc_id", id)
		orig, _ := d.origins.get(cardChat, cardMsg)
		d.editCard(ctx, b, cardChat, cardMsg, card.Doc, card.CategoryFolderID, orig)
	}
}

// onDeleteAsk asks to confirm before deleting.
func (d *docs) onDeleteAsk(ctx context.Context, b *bot.Bot, u *models.Update, parts []string) {
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
	d.editMessage(ctx, b, chatID, msgID, textDeleteConfirm, kb)
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
	if err := d.svc.Delete(ctx, user, id); err != nil {
		d.fail(ctx, b, chatID, "delete", err)
		return
	}
	d.log.InfoContext(ctx, "document deleted", "user_id", user, "doc_id", id)
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
			d.editText(ctx, b, chatID, msgID, textDeleted)
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
		d.editText(ctx, b, chatID, msgID, textDeleted)
	}
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
func (d *docs) sendFile(ctx context.Context, b *bot.Bot, chatID int64, doc docsbot.Document) {
	var err error
	caption := doc.Title + " · " + categoryLabel(doc.Category)
	op := "sendDocument"
	start := time.Now()
	switch doc.Kind {
	case docsbot.KindPhoto:
		op = "sendPhoto"
		_, err = b.SendPhoto(ctx, &bot.SendPhotoParams{ChatID: chatID, Photo: &models.InputFileString{Data: doc.TgFileID}, Caption: caption})
	default:
		_, err = b.SendDocument(ctx, &bot.SendDocumentParams{ChatID: chatID, Document: &models.InputFileString{Data: doc.TgFileID}, Caption: caption})
	}
	traceAPI(ctx, d.log, op, start)
	if err != nil {
		d.log.ErrorContext(ctx, "send file", "err", err, "user_id", doc.UserID, "doc_id", doc.ID)
		reply(ctx, b, d.log, chatID, textSendFailed)
		return
	}
	d.log.InfoContext(ctx, "document sent", "user_id", doc.UserID, "doc_id", doc.ID)
}

func (d *docs) sendCard(ctx context.Context, b *bot.Bot, chatID int64, doc docsbot.Document, header, folderID string) {
	text := cardText(doc)
	if header != "" {
		text = header + "\n\n" + text
	}
	d.send(ctx, b, chatID, text, cardKeyboard(doc, folderID, ""))
}

// editCard replaces a message with a document's card and remembers where the
// card was opened from (orig) so "Назад" and returns survive later edits.
func (d *docs) editCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, doc docsbot.Document, folderID, orig string) {
	d.editMessage(ctx, b, chatID, msgID, cardText(doc), cardKeyboard(doc, folderID, orig))
	d.origins.set(chatID, msgID, orig)
}

// gone replaces a message about a vanished document with a way back to the catalog.
func (d *docs) gone(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnMyDocs, CallbackData: cbPrefix + actListBack},
	}}}
	d.editMessage(ctx, b, chatID, msgID, textGone, kb)
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
	card, err := d.svc.Card(ctx, user, id)
	if errors.Is(err, docsbot.ErrNotFound) {
		d.gone(ctx, b, chatID, msgID)
		return
	}
	if err != nil {
		d.fail(ctx, b, chatID, "load card", err)
		return
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

// send posts a message, with an inline keyboard when kb is non-nil.
func (d *docs) send(ctx context.Context, b *bot.Bot, chatID int64, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.SendMessageParams{ChatID: chatID, Text: text}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	_, err := b.SendMessage(ctx, params)
	traceAPI(ctx, d.log, "sendMessage", start)
	if err != nil {
		d.log.Error("send telegram message", "err", err)
	}
}

// editMessage rewrites a message's text and keyboard in place (used for paging).
func (d *docs) editMessage(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, kb *models.InlineKeyboardMarkup) {
	params := &bot.EditMessageTextParams{ChatID: chatID, MessageID: msgID, Text: text}
	if kb != nil {
		params.ReplyMarkup = kb
	}
	start := time.Now()
	_, err := b.EditMessageText(ctx, params)
	traceAPI(ctx, d.log, "editMessageText", start)
	if err != nil {
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

// deepLinkConnected reports whether /start carried the ?start=connected payload.
func deepLinkConnected(text string) bool {
	_, arg, _ := strings.Cut(text, " ")
	return strings.TrimSpace(arg) == "connected"
}

// renameStore remembers which document a user is renaming. In memory only: a
// restart drops it, and a stale arm (older than the TTL) is ignored.
type renameStore struct {
	mu  sync.Mutex
	m   map[int64]renameEntry
	now func() time.Time
}

type renameEntry struct {
	id   docsbot.DocumentID
	chat int64 // the card's message, edited in place after the rename
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

// take removes and returns the armed document, if any and not expired.
func (s *renameStore) take(user int64) (id docsbot.DocumentID, chat int64, msg int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.m[user]
	if !found {
		return "", 0, 0, false
	}
	delete(s.m, user)
	if s.now().Sub(e.at) > renameTTL {
		return "", 0, 0, false
	}
	return e.id, e.chat, e.msg, true
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

// exportPause spaces out resent files to stay under Telegram's rate limit.
const exportPause = 50 * time.Millisecond

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
