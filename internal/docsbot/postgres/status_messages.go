package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/alexe0110/most-app/internal/docsbot"
)

var _ docsbot.StatusMessages = (*Repo)(nil)

// OpenForUser returns the user's newest unclosed status message, if any.
func (r *Repo) OpenForUser(ctx context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.StatusMessage{}, false, err
	}
	row, err := r.GetOpenStatusMessageForUser(ctx, uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return docsbot.StatusMessage{}, false, nil
	}
	if err != nil {
		return docsbot.StatusMessage{}, false, fmt.Errorf("get open status message: %w", err)
	}
	return toStatusMessage(row), true, nil
}

// Create stores a new open status message.
func (r *Repo) Create(ctx context.Context, user docsbot.UserID, chatID int64, messageID int) (docsbot.StatusMessage, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.StatusMessage{}, err
	}
	row, err := r.InsertStatusMessage(ctx, InsertStatusMessageParams{UserID: uid, ChatID: chatID, MessageID: int64(messageID)})
	if err != nil {
		return docsbot.StatusMessage{}, fmt.Errorf("insert status message: %w", err)
	}
	return toStatusMessage(row), nil
}

// Close marks the status message closed.
func (r *Repo) Close(ctx context.Context, id docsbot.StatusMessageID) error {
	sid, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	if err := r.CloseStatusMessage(ctx, sid); err != nil {
		return fmt.Errorf("close status message: %w", err)
	}
	return nil
}

// SetMessageID records the Telegram message id after the message was resent.
func (r *Repo) SetMessageID(ctx context.Context, id docsbot.StatusMessageID, messageID int) error {
	sid, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	if err := r.UpdateStatusMessageMessageID(ctx, UpdateStatusMessageMessageIDParams{ID: sid, MessageID: int64(messageID)}); err != nil {
		return fmt.Errorf("update status message id: %w", err)
	}
	return nil
}

// Attach adds a document to the status message.
func (r *Repo) Attach(ctx context.Context, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	did, err := parseUUID(string(doc))
	if err != nil {
		return err
	}
	sid, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	if err := r.AttachDocumentToStatus(ctx, AttachDocumentToStatusParams{ID: did, StatusMessageID: sid}); err != nil {
		return fmt.Errorf("attach document to status: %w", err)
	}
	return nil
}

// DocumentsIn lists the documents attached to the status message, oldest first.
func (r *Repo) DocumentsIn(ctx context.Context, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	sid, err := parseUUID(string(id))
	if err != nil {
		return nil, err
	}
	rows, err := r.ListDocumentsByStatusMessage(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("list status documents: %w", err)
	}
	return toDocuments(rows), nil
}

// ByID returns the status message, or ErrNotFound.
func (r *Repo) ByID(ctx context.Context, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	sid, err := parseUUID(string(id))
	if err != nil {
		return docsbot.StatusMessage{}, err
	}
	row, err := r.GetStatusMessageByID(ctx, sid)
	if err != nil {
		return docsbot.StatusMessage{}, notFoundOr(err, "get status message")
	}
	return toStatusMessage(row), nil
}

func toStatusMessage(m StatusMessage) docsbot.StatusMessage {
	return docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(m.ID.String()),
		UserID:    docsbot.UserID(m.UserID.String()),
		ChatID:    m.ChatID,
		MessageID: int(m.MessageID),
		CreatedAt: m.CreatedAt.Time,
		Closed:    m.ClosedAt.Valid,
	}
}
