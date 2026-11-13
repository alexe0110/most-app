package telegram

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

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

		driveFolder: gdrive.DefaultRootFolder,

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
	d.out(b).send(ctx, u.Message.Chat.ID, textUnsupported)
}

// onText handles any non-command text: a pending rename, otherwise a search. A
// text sent after the rename expired (or the bot restarted) is just a search.
func (d *docs) onText(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	chatID := u.Message.Chat.ID
	if strings.HasPrefix(u.Message.Text, "/") {
		d.out(b).send(ctx, chatID, textUnknown)
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

// callback dispatches an inline-button press by its action.
func (d *docs) callback(ctx context.Context, b *bot.Bot, u *models.Update) {
	parts := strings.Split(u.CallbackQuery.Data, ":") // d:<action>[:docID[:arg]]
	if !answersItself(parts) {
		d.out(b).answer(ctx, u.CallbackQuery.ID, withToast(toastFor(parts)))
	}
	user, ok := userID(ctx)
	if !ok {
		if answersItself(parts) {
			d.out(b).answer(ctx, u.CallbackQuery.ID) // never leave the button spinning
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

// out is the one way this handler talks to Telegram's message API.
func (d *docs) out(b *bot.Bot) sender { return sender{api: b, log: d.log} }

// fail logs the real error once and shows the user a generic message.
func (d *docs) fail(ctx context.Context, b *bot.Bot, chatID int64, what string, err error) {
	if user, ok := userID(ctx); ok {
		d.log.ErrorContext(ctx, what, "err", err, "user_id", user)
	} else {
		d.log.ErrorContext(ctx, what, "err", err)
	}
	d.out(b).send(ctx, chatID, textError)
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
