package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/oauth"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

// userLookup resolves an internal user id to the Telegram chat to notify.
// Declared here, at the consumer; docsbot/postgres implements it.
type userLookup interface {
	TgUserID(ctx context.Context, userID string) (int64, error)
}

// driveCounter is the part of docsbot.Service the connect callback needs: it
// resets the user's cached Drive folders and counts documents not yet on Drive.
type driveCounter interface {
	ResetDriveFolders(ctx context.Context, user docsbot.UserID) error
	NotBackedUpCount(ctx context.Context, user docsbot.UserID) (int, error)
}

// NotifyConnected builds the OnConnected callback: after Drive is linked it tells
// the user, and offers to transfer any documents still only in Telegram.
func NotifyConnected(b *tg.Bot, users userLookup, counter driveCounter, log *slog.Logger) oauth.OnConnected {
	return func(ctx context.Context, userID string) error {
		chatID, err := users.TgUserID(ctx, userID)
		if err != nil {
			return fmt.Errorf("resolve tg user: %w", err)
		}
		if err := counter.ResetDriveFolders(ctx, docsbot.UserID(userID)); err != nil {
			return fmt.Errorf("reset drive folders: %w", err)
		}
		log.InfoContext(ctx, "drive folder cache reset", "user_id", userID, "reason", "reconnect")
		if err := b.Send(ctx, chatID, textDriveConnected); err != nil {
			return err
		}
		log.InfoContext(ctx, "drive connected", "user_id", userID)
		n, err := counter.NotBackedUpCount(ctx, docsbot.UserID(userID))
		if err != nil {
			return fmt.Errorf("count not backed up: %w", err)
		}
		if n == 0 {
			return nil
		}
		return b.SendMarkup(ctx, chatID, fmt.Sprintf(textTransferOffer, n), transferOfferKeyboard())
	}
}

// statusService is the part of docsbot.Service the Notifier needs to refresh
// batch status messages. Declared here, at the consumer.
type statusService interface {
	StatusView(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) (docsbot.StatusView, error)
	CloseStatus(ctx context.Context, id docsbot.StatusMessageID) error
	SetStatusMessageID(ctx context.Context, id docsbot.StatusMessageID, messageID int) error
}

// editInterval is the minimum gap between edits of one status message, to stay
// under Telegram's per-chat rate limits.
const editInterval = time.Second

// Notifier posts upload outcomes to the user in Telegram. It satisfies
// docsbot/jobs.Notifier (checked where it is bound to the worker in cmd).
type Notifier struct {
	bot   *tg.Bot
	users userLookup
	auth  Authorizer
	svc   statusService
	log   *slog.Logger

	mu        sync.Mutex
	lastEdit  map[docsbot.StatusMessageID]time.Time
	revokedAt map[docsbot.UserID]time.Time // last reconnect prompt per user
}

// reconnectDebounce limits the reconnect prompt to once per user per this period,
// so a revoked grant does not prompt for every document of a batch.
const reconnectDebounce = 10 * time.Minute

// NewNotifier builds the Telegram Notifier that the upload worker reports to.
func NewNotifier(b *tg.Bot, users userLookup, auth Authorizer, svc statusService, log *slog.Logger) *Notifier {
	return &Notifier{bot: b, users: users, auth: auth, svc: svc, log: log,
		lastEdit: map[docsbot.StatusMessageID]time.Time{}, revokedAt: map[docsbot.UserID]time.Time{}}
}

// UploadOutcome reflects a document's final upload state: it refreshes the batch
// status message, or, for a document outside one, sends a single notice.
func (n *Notifier) UploadOutcome(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID) error {
	view, err := n.svc.StatusView(ctx, user, doc)
	if err != nil {
		n.log.ErrorContext(ctx, "load status view", "err", err, "user_id", user, "doc_id", doc)
		return fmt.Errorf("status view: %w", err)
	}
	if !view.Grouped {
		return n.singleOutcome(ctx, user, view.Doc)
	}
	text, kb := renderStatusMessage(view.Message, view.Lines, view.FolderByCat)
	n.throttle(view.Message.ID)
	err = n.bot.EditHTML(ctx, view.Message.ChatID, view.Message.MessageID, text, kb)
	switch msg := strings.ToLower(errString(err)); {
	case err == nil, strings.Contains(msg, "message is not modified"):
	case strings.Contains(msg, "message to edit not found"), strings.Contains(msg, "message can't be edited"):
		mid, serr := n.bot.SendHTML(ctx, view.Message.ChatID, text, kb)
		if serr != nil {
			n.log.ErrorContext(ctx, "resend status message", "err", serr, "user_id", user)
		} else if uerr := n.svc.SetStatusMessageID(ctx, view.Message.ID, mid); uerr != nil {
			n.log.ErrorContext(ctx, "save status message id", "err", uerr, "user_id", user)
		}
	default:
		n.log.ErrorContext(ctx, "edit status message", "err", err, "user_id", user)
	}
	if view.AllTerminal {
		n.mu.Lock()
		delete(n.lastEdit, view.Message.ID)
		n.mu.Unlock()
		if cerr := n.svc.CloseStatus(ctx, view.Message.ID); cerr != nil {
			n.log.ErrorContext(ctx, "close status message", "err", cerr, "user_id", user)
		}
	}
	return nil
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
	return n.bot.SendMarkup(ctx, chatID, text, kb)
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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Reconnect asks the user to re-link Drive after access was revoked. At most one
// prompt is delivered per reconnectDebounce; a failed send does not count, so a
// retry can still reach the user.
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
	if url, uErr := n.auth.AuthURL(ctx, string(user), botName); uErr != nil {
		n.log.Error("build auth url", "err", uErr)
		err = n.bot.Send(ctx, chatID, textReconnect)
	} else {
		err = n.bot.SendMarkup(ctx, chatID, textReconnect, reconnectKeyboard(url))
	}
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.revokedAt[user] = time.Now()
	n.mu.Unlock()
	return nil
}
