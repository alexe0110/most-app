package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// statusService is the part of docsbot.Service the Notifier needs to refresh
// batch status messages. Declared here, at the consumer.
type statusService interface {
	Card(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Card, error)
	StatusView(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) (docsbot.StatusView, error)
	CloseStatus(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error
	SetStatusMessageID(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error
}

// editInterval is the minimum gap between edits of one status message, to stay
// under Telegram's per-chat rate limits.
const editInterval = time.Second

// Notifier posts upload outcomes to the user in Telegram. It satisfies
// docsbot/jobs.Notifier (checked where it is bound to the worker in cmd).
type Notifier struct {
	out   sender
	users userLookup
	auth  Authorizer
	svc   statusService
	log   *slog.Logger

	renames *renameStore // shared with the docs handlers; nil when not wired

	mu        sync.Mutex
	lastEdit  map[docsbot.StatusMessageID]time.Time
	revokedAt map[docsbot.UserID]time.Time // last reconnect prompt per user
}

// reconnectDebounce limits the reconnect prompt to once per user per this period,
// so a revoked grant does not prompt for every document of a batch.
const reconnectDebounce = 10 * time.Minute

// NewNotifier builds the Telegram Notifier that the upload worker reports to.
func NewNotifier(b *tg.Bot, users userLookup, auth Authorizer, svc statusService, log *slog.Logger, opts ...NotifierOption) *Notifier {
	n := &Notifier{out: sender{api: b.Client(), log: log}, users: users, auth: auth, svc: svc, log: log,
		lastEdit: map[docsbot.StatusMessageID]time.Time{}, revokedAt: map[docsbot.UserID]time.Time{}}
	for _, opt := range opts {
		opt(n)
	}
	return n
}

// NotifierOption tunes a Notifier at construction.
type NotifierOption func(*Notifier)

// WithNotifierRenames shares the rename state with the docs handlers (see Renames).
func WithNotifierRenames(r *Renames) NotifierOption {
	return func(n *Notifier) { n.renames = r.store }
}

// dropRename ends a rename prompt shown on a message the Notifier is about to redraw.
func (n *Notifier) dropRename(chat int64, msg int) {
	if n.renames != nil {
		n.renames.dropMessage(chat, msg)
	}
}

// UploadOutcome reflects a document's final upload state: it refreshes the batch
// status message, or, for a document outside one, sends a single notice. It only
// returns errors; the worker logs them.
func (n *Notifier) UploadOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error {
	view, err := n.svc.StatusView(ctx, user, doc)
	if err != nil {
		return fmt.Errorf("status view: %w", err)
	}
	if !view.Grouped {
		return n.singleOutcome(ctx, user, view.Doc)
	}
	text, kb := renderStatusMessage(view.Message, view.Lines, view.FolderByCat)
	n.throttle(view.Message.ID)
	n.dropRename(view.Message.ChatID, view.Message.MessageID) // the redraw overwrites a prompt shown here
	err = n.out.edit(ctx, view.Message.ChatID, view.Message.MessageID, text, withHTML(), withKeyboard(kb), quiet())
	n.dropRename(view.Message.ChatID, view.Message.MessageID)
	var errs []error
	switch err = n.out.ignoreBlocked(ctx, err); {
	case err == nil:
	case isMessageGone(err):
		mid, serr := n.out.send(ctx, view.Message.ChatID, text, withHTML(), withKeyboard(kb), quiet())
		if serr = n.out.ignoreBlocked(ctx, serr); serr != nil {
			errs = append(errs, fmt.Errorf("resend status message: %w", serr))
		} else if mid != 0 { // 0: the resend hit a blocked user
			if uerr := n.svc.SetStatusMessageID(ctx, user, view.Message.ID, mid); uerr != nil {
				errs = append(errs, fmt.Errorf("save status message id: %w", uerr))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("edit status message: %w", err))
	}
	if view.AllTerminal {
		n.mu.Lock()
		delete(n.lastEdit, view.Message.ID)
		n.mu.Unlock()
		if cerr := n.svc.CloseStatus(ctx, user, view.Message.ID); cerr != nil {
			errs = append(errs, fmt.Errorf("close status message: %w", cerr))
		}
	}
	return errors.Join(errs...)
}

// singleOutcome is the per-document notice for a document without a status message.
func (n *Notifier) singleOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.Document) error {
	var (
		text string
		kb   *models.InlineKeyboardMarkup
	)
	switch doc.BackupStatus {
	case docsbot.BackupBackedUp:
		text, kb = fmt.Sprintf(textBackedUp, doc.Title), openDriveKeyboard(doc)
	case docsbot.BackupFailed:
		text, kb = textBackupFailed, retryKeyboard(doc.ID)
	default:
		return nil
	}
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	_, err = n.out.send(ctx, chatID, text, withKeyboard(kb), quiet())
	return err
}

// SyncOutcome shows the document's card after a cloud sync failed or found the
// Drive file gone, with the matching button (retry or re-upload).
func (n *Notifier) SyncOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error {
	card, err := n.svc.Card(ctx, user, doc)
	if errors.Is(err, docsbot.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load card: %w", err)
	}
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	var url string
	if revoked(card.Doc) {
		var uErr error
		if url, uErr = n.auth.AuthURL(ctx, string(user), docsbot.BotName); uErr != nil {
			n.log.ErrorContext(ctx, "build auth url", "err", uErr)
			url = ""
		}
	}
	_, err = n.out.send(ctx, chatID, cardText(card.Doc), withHTML(), withKeyboard(cardKeyboardWith(card.Doc, "", "", url)), quiet())
	return n.out.ignoreBlocked(ctx, err)
}

// RemoteKept tells the user a deleted document's Drive file could not be trashed.
func (n *Notifier) RemoteKept(ctx context.Context, user docsbot.UserID) error {
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	_, err = n.out.send(ctx, chatID, textDeletedRemoteKept, quiet())
	return n.out.ignoreBlocked(ctx, err)
}

// throttle sleeps so consecutive edits of one status message are at least
// editInterval apart. The upload queue runs one worker, so callers are serial.
func (n *Notifier) throttle(id docsbot.StatusMessageID) {
	n.mu.Lock()
	last := n.lastEdit[id]
	n.mu.Unlock()
	if wait := editInterval - time.Since(last); wait > 0 && !last.IsZero() {
		time.Sleep(wait)
	}
	n.mu.Lock()
	n.lastEdit[id] = time.Now()
	n.mu.Unlock()
}

// Reconnect asks the user to re-link Drive after access was revoked. At most one
// prompt is delivered per reconnectDebounce; a failed send does not count, so a
// retry can still reach the user. A blocked bot does count: retrying on every
// upload would not help.
func (n *Notifier) Reconnect(ctx context.Context, user docsbot.UserID) error {
	n.mu.Lock()
	last := n.revokedAt[user]
	expired := !last.IsZero() && time.Since(last) >= reconnectDebounce
	if expired {
		delete(n.revokedAt, user) // keep the map bounded
	}
	n.mu.Unlock()
	if !last.IsZero() && !expired {
		return nil
	}
	chatID, err := n.users.TgUserID(ctx, string(user))
	if err != nil {
		return fmt.Errorf("resolve tg user: %w", err)
	}
	if url, uErr := n.auth.AuthURL(ctx, string(user), docsbot.BotName); uErr != nil {
		n.log.ErrorContext(ctx, "build auth url", "err", uErr)
		_, err = n.out.send(ctx, chatID, textReconnect, quiet())
	} else {
		_, err = n.out.send(ctx, chatID, textReconnect, withKeyboard(reconnectKeyboard(url)), quiet())
	}
	if err = n.out.ignoreBlocked(ctx, err); err != nil {
		return err
	}
	n.mu.Lock()
	n.revokedAt[user] = time.Now()
	n.mu.Unlock()
	return nil
}
