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
	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
	"github.com/alexe0110/most-app/internal/platform/gdrive"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// docs is the inbound adapter for the core document flow: it turns updates into
// Service calls and renders the replies. State is the domain's; the only local
// state is which document a user is renaming (force-reply).
type docs struct {
	svc  *docsbot.Service
	auth Authorizer
	log  *slog.Logger
	// renames: the document a user is renaming and the message showing the prompt.
	renames *tg.TTLMap[int64, renameEntry]
	// confirms: users asked to confirm data deletion; a stale arm just means the
	// user taps "Удалить мои данные" again.
	confirms *tg.TTLMap[int64, struct{}]
	// searches: a user's last search query, for "Назад" from a card opened from the
	// results. get keeps the entry so repeated presses work.
	searches *tg.TTLMap[int64, string]
	// origins: where each card message was opened from, see originKey.
	origins *tg.TTLMap[originKey, string]
	// settingsMsgs remembers the /settings message per chat for the OAuth return.
	settingsMsgs *tg.TTLMap[int64, int]
	albums       *albumBuffer
	// feedbackChat is where feedback goes and replies come from; zero: feedback is off.
	feedbackChat int64
	// feedbackPending: users whose next message is feedback. A lost entry (restart,
	// TTL) only means the next message is handled as usual.
	feedbackPending *tg.TTLMap[int64, feedbackEntry]
	// feedbackGroups: albums being sent as feedback, keyed by feedbackGroupKey, so
	// the parts after the first find the mode that the first one took.
	feedbackGroups *tg.TTLMap[string, feedbackEntry]
	feedbackClaim  sync.Mutex // makes taking the mode for an album atomic
	feedbackAlbums *feedbackBuffer
	// driveFolder is the Drive root folder name shown in /start and /settings.
	driveFolder string
	// botUsername is the bot's @username (without @), for the inline-mode hints; may be empty.
	botUsername string
	// privacyURL is the privacy page linked from /help and /settings; empty leaves the line out.
	privacyURL string
	// albumPDFs makes the "one PDF" offer of an album; set by Register (the
	// converter owns the PDF work). Nil: albums are always saved one by one.
	albumPDFs albumPDFs
	// pauseFile and pauseAlbum space out export sends under the Bot API rate limit.
	pauseFile, pauseAlbum time.Duration
	// exports holds the users with an export in flight: one at a time per user.
	exportsMu sync.Mutex
	exports   map[docsbot.UserID]bool
}

func newDocs(svc *docsbot.Service, auth Authorizer, log *slog.Logger, opts ...Option) *docs {
	d := &docs{
		svc: svc, auth: auth, log: log,
		renames:      tg.NewTTLMap[int64, renameEntry](renameTTL, tg.MaxStoreEntries, time.Now),
		confirms:     tg.NewTTLMap[int64, struct{}](confirmTTL, tg.MaxStoreEntries, time.Now),
		searches:     tg.NewTTLMap[int64, string](searchTTL, tg.MaxStoreEntries, time.Now),
		origins:      tg.NewTTLMap[originKey, string](originTTL, tg.MaxStoreEntries, time.Now),
		settingsMsgs: tg.NewTTLMap[int64, int](settingsTTL, tg.MaxStoreEntries, time.Now),
		albums:       newAlbumBuffer(),

		feedbackPending: tg.NewTTLMap[int64, feedbackEntry](feedbackTTL, tg.MaxStoreEntries, time.Now),
		feedbackGroups:  tg.NewTTLMap[string, feedbackEntry](feedbackGroupTTL, tg.MaxStoreEntries, time.Now),
		feedbackAlbums:  newFeedbackBuffer(),

		driveFolder: gdrive.DefaultRootFolder,

		pauseFile: exportPause, pauseAlbum: exportPerFilePause,
		exports: map[docsbot.UserID]bool{},
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// albumPDFs remembers an album's images for the "one PDF" button and returns the
// button's token. Declared here, at the consumer; the converter implements it.
type albumPDFs interface {
	RegisterAlbum(user docsbot.UserID, tgUser int64, group string, images []converter.AlbumImage) string
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
	return u.Message != nil && unsupportedMedia(u.Message)
}

// unsupportedMedia reports media the bot neither stores nor takes as feedback.
func unsupportedMedia(m *models.Message) bool {
	return m.Voice != nil || m.Video != nil || m.VideoNote != nil || m.Audio != nil ||
		m.Sticker != nil || m.Animation != nil || m.Contact != nil || m.Location != nil || m.Poll != nil
}

// onUnsupported tells the user which media the bot takes.
func (d *docs) onUnsupported(ctx context.Context, b *bot.Bot, u *models.Update) {
	d.out(b).Send(ctx, u.Message.Chat.ID, textUnsupported)
}

// onText handles any non-command text: a pending rename, otherwise a search. A
// text sent after the rename expired (or the bot restarted) is just a search.
func (d *docs) onText(ctx context.Context, b *bot.Bot, u *models.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	chatID := u.Message.Chat.ID
	if strings.HasPrefix(u.Message.Text, "/") {
		d.out(b).Send(ctx, chatID, textUnknown)
		return
	}
	user, ok := userID(ctx)
	if !ok {
		return
	}
	if _, armed := d.confirms.Take(u.Message.From.ID); armed {
		d.onDeleteConfirm(ctx, b, chatID, user, u.Message.Text)
		return
	}
	if e, ok := d.renames.Take(u.Message.From.ID); ok {
		d.applyRename(ctx, b, user, e, u.Message)
		return
	}
	if isConfirmWord(u.Message.Text) {
		// The confirmation lapsed (or the bot restarted): do not search for the word.
		d.out(b).Send(ctx, chatID, textDeleteExpired)
		return
	}
	d.searches.Set(u.Message.From.ID, u.Message.Text)
	d.runSearch(ctx, b, chatID, user, u.Message.Text)
}

// callback dispatches an inline-button press by its action.
func (d *docs) callback(ctx context.Context, b *bot.Bot, u *models.Update) {
	p := newPress(ctx, u.CallbackQuery)
	if !answersItself(p.data) {
		d.out(b).Answer(ctx, p.queryID, tg.WithToast(toastFor(p.data)))
	}
	if !p.hasUser {
		if answersItself(p.data) {
			d.out(b).Answer(ctx, p.queryID) // never leave the button spinning
		}
		return
	}
	if p.data.action != actFeedback && p.data.action != actFeedbackCancel {
		d.feedbackPending.Delete(p.tgUser) // any other button abandons the feedback prompt
	}
	if p.data.action != actRenameCancel {
		// Any other button abandons a rename prompt that sits on another message.
		d.releaseRename(ctx, b, p)
	}
	switch p.data.action {
	case actCategory:
		d.onCategory(ctx, b, p)
	case actCategoryGroup:
		d.onCategoryGroup(ctx, b, p)
	case actGroupPick:
		d.onGroupPick(ctx, b, p)
	case actSetCategory:
		d.onSetCategory(ctx, b, p)
	case actChangeCat:
		d.onChangeCat(ctx, b, p)
	case actCardBack:
		d.onCardBack(ctx, b, p)
	case actSend:
		d.onSend(ctx, b, p)
	case actRename:
		d.onRenamePrompt(ctx, b, p)
	case actRenameCancel:
		d.onRenameCancel(ctx, b, p)
	case actDeleteAsk:
		d.onDeleteAsk(ctx, b, p)
	case actDeleteYes:
		d.onDeleteYes(ctx, b, p)
	case actDeleteNo:
		d.onDeleteNo(ctx, b, p)
	case actListCat:
		d.onListCat(ctx, b, p)
	case actOpenCard:
		d.onOpenCard(ctx, b, p)
	case actSearchBack:
		d.onSearchBack(ctx, b, p)
	case actListBack:
		d.onListBack(ctx, b, p)
	case actTransfer, actTransferYes:
		d.onTransfer(ctx, b, p)
	case actTransferNo:
		d.onTransferNo(ctx, b, p)
	case actRetry:
		d.onRetry(ctx, b, p)
	case actRetrySync:
		d.onRetrySync(ctx, b, p)
	case actDisconnectAsk:
		d.onDisconnectAsk(ctx, b, p)
	case actDisconnect:
		d.onDisconnect(ctx, b, p)
	case actDisconnectNo:
		d.onDisconnectNo(ctx, b, p)
	case actConnectAsk:
		d.onConnectAsk(ctx, b, p)
	case actConnectNo:
		d.onConnectNo(ctx, b, p)
	case actRetryAll:
		d.onRetryAll(ctx, b, p)
	case actExport:
		d.onExport(ctx, b, p)
	case actDeleteData:
		d.onDeleteData(ctx, b, p)
	case actDeleteDataNo:
		d.onDeleteDataNo(ctx, b, p)
	case actRetryBatch:
		d.onRetryBatch(ctx, b, p)
	case actInline:
		d.onInlineSwitch(ctx, b, p)
	case actFeedback:
		d.onFeedbackButton(ctx, b, p)
	case actFeedbackCancel:
		d.onFeedbackCancel(ctx, b, p)
	case actNoop:
	}
}

// answersItself reports whether the action handler answers the callback query
// on its own (to pick the toast from the outcome).
func answersItself(c callbackData) bool {
	return c.action == actRetry || c.action == actRetrySync
}

// toastFor is the short pop-up shown for an action, "" for none.
func toastFor(c callbackData) string {
	switch c.action {
	case actSend:
		return toastSending
	case actSetCategory, actDeleteYes:
		return toastDone
	}
	return ""
}

// out is the one way this handler talks to Telegram's message API.
func (d *docs) out(b *bot.Bot) tg.Sender { return tg.NewSender(b, d.log) }

// fail logs the real error once and shows the user a generic message.
func (d *docs) fail(ctx context.Context, b *bot.Bot, chatID int64, what string, err error) {
	if user, ok := userID(ctx); ok {
		d.log.ErrorContext(ctx, what, "err", err, "user_id", user)
	} else {
		d.log.ErrorContext(ctx, what, "err", err)
	}
	d.out(b).Send(ctx, chatID, textError, tg.WithKeyboard(d.problemKeyboard(strings.ReplaceAll(what, " ", "_"))))
}

// userID reads the internal user id the ensureUser middleware put in ctx.
func userID(ctx context.Context) (docsbot.UserID, bool) {
	id, ok := tg.UserIDFromContext(ctx)
	return docsbot.UserID(id), ok
}
