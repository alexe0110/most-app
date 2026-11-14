package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// StatusMessages persists the per-batch upload status messages; it implements
// docsbot.StatusMessages.
type StatusMessages struct{ q *Queries }

var _ docsbot.StatusMessages = (*StatusMessages)(nil)

// NewStatusMessages wires StatusMessages around an existing pool.
func NewStatusMessages(pool *pgxpool.Pool) *StatusMessages {
	return &StatusMessages{q: New(pool)}
}

// OpenForUser returns the user's newest unclosed status message, if any.
func (r *StatusMessages) OpenForUser(ctx context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.StatusMessage{}, false, err
	}
	row, err := r.q.GetOpenStatusMessageForUser(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return docsbot.StatusMessage{}, false, nil
	}
	if err != nil {
		return docsbot.StatusMessage{}, false, fmt.Errorf("get open status message: %w", err)
	}
	return toStatusMessage(row), true, nil
}

// Create stores a new open status message.
func (r *StatusMessages) Create(ctx context.Context, user docsbot.UserID, chatID int64, messageID int, kind docsbot.StatusKind) (docsbot.StatusMessage, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.StatusMessage{}, err
	}
	row, err := r.q.InsertStatusMessage(ctx, InsertStatusMessageParams{UserID: uid, ChatID: chatID, MessageID: int64(messageID), Kind: string(kind)})
	if err != nil {
		return docsbot.StatusMessage{}, fmt.Errorf("insert status message: %w", err)
	}
	return toStatusMessage(row), nil
}

// Close marks the status message closed.
func (r *StatusMessages) Close(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error {
	sid, uid, err := statusIDs(id, user)
	if err != nil {
		return err
	}
	if err := r.q.CloseStatusMessage(ctx, CloseStatusMessageParams{ID: sid, UserID: uid}); err != nil {
		return fmt.Errorf("close status message: %w", err)
	}
	return nil
}

// SetMessageID records the Telegram message id after the message was resent.
func (r *StatusMessages) SetMessageID(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error {
	sid, uid, err := statusIDs(id, user)
	if err != nil {
		return err
	}
	if err := r.q.UpdateStatusMessageMessageID(ctx, UpdateStatusMessageMessageIDParams{ID: sid, UserID: uid, MessageID: int64(messageID)}); err != nil {
		return fmt.Errorf("update status message id: %w", err)
	}
	return nil
}

// Attach adds a document to the status message.
func (r *StatusMessages) Attach(ctx context.Context, user docsbot.UserID, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	did, uid, err := ids(doc, user)
	if err != nil {
		return err
	}
	sid, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	if err := r.q.AttachDocumentToStatus(ctx, AttachDocumentToStatusParams{ID: did, UserID: uid, StatusMessageID: sid}); err != nil {
		return fmt.Errorf("attach document to status: %w", err)
	}
	return nil
}

// DocumentsIn lists the documents attached to the status message, oldest first.
func (r *StatusMessages) DocumentsIn(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	sid, uid, err := statusIDs(id, user)
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListDocumentsByStatusMessage(ctx, ListDocumentsByStatusMessageParams{StatusMessageID: sid, UserID: uid})
	if err != nil {
		return nil, fmt.Errorf("list status documents: %w", err)
	}
	return toDocuments(rows), nil
}

// ByID returns the user's status message, or ErrNotFound (also for another user's).
func (r *StatusMessages) ByID(ctx context.Context, user docsbot.UserID, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	sid, uid, err := statusIDs(id, user)
	if err != nil {
		return docsbot.StatusMessage{}, err
	}
	row, err := r.q.GetStatusMessageByID(ctx, GetStatusMessageByIDParams{ID: sid, UserID: uid})
	if err != nil {
		return docsbot.StatusMessage{}, notFoundOr(err, "get status message")
	}
	return toStatusMessage(row), nil
}

// PurgeClosed deletes every user's status messages closed before olderThan.
func (r *StatusMessages) PurgeClosed(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := r.q.DeleteClosedStatusMessages(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete closed status messages: %w", err)
	}
	return int(n), nil
}

// statusIDs parses a (status message, user) id pair in one step.
func statusIDs(id docsbot.StatusMessageID, user docsbot.UserID) (sid, uid pgtype.UUID, err error) {
	if sid, err = parseUUID(string(id)); err != nil {
		return
	}
	uid, err = parseUUID(string(user))
	return
}

func toStatusMessage(m StatusMessage) docsbot.StatusMessage {
	return docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(m.ID.String()),
		UserID:    docsbot.UserID(m.UserID.String()),
		ChatID:    m.ChatID,
		MessageID: int(m.MessageID),
		CreatedAt: m.CreatedAt.Time,
		Closed:    m.ClosedAt.Valid,
		Kind:      docsbot.StatusKind(m.Kind),
	}
}
