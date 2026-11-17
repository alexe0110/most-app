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

// Feedback stores the links between feedback-chat messages and users in the
// platform feedback_messages table; it implements the domain's Feedback port.
type Feedback struct {
	q *Queries
}

var _ docsbot.Feedback = (*Feedback)(nil)

// NewFeedback wires Feedback around an existing pool.
func NewFeedback(pool *pgxpool.Pool) *Feedback {
	return &Feedback{q: New(pool)}
}

// RecordFeedback stores all messages of the request with one statement.
func (r *Feedback) RecordFeedback(ctx context.Context, rec docsbot.FeedbackRecord) error {
	uid, err := parseUUID(string(rec.User))
	if err != nil {
		return err
	}
	ids := make([]int64, len(rec.MessageIDs))
	for i, id := range rec.MessageIDs {
		ids[i] = int64(id)
	}
	err = r.q.InsertFeedbackMessages(ctx, InsertFeedbackMessagesParams{UserID: uid, ChatID: rec.ChatID, ChatMessageIds: ids})
	if err != nil {
		return fmt.Errorf("insert feedback messages: %w", err)
	}
	return nil
}

// CountFeedbackSince counts the user's requests since the given time.
func (r *Feedback) CountFeedbackSince(ctx context.Context, user docsbot.UserID, since time.Time) (int, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return 0, err
	}
	n, err := r.q.CountFeedbackSince(ctx, CountFeedbackSinceParams{
		UserID: uid, CreatedAt: pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("count feedback: %w", err)
	}
	return int(n), nil
}

// FeedbackAuthor returns the Telegram id of the user behind a feedback-chat message.
func (r *Feedback) FeedbackAuthor(ctx context.Context, chatID int64, msgID int) (int64, error) {
	tgUser, err := r.q.FeedbackAuthor(ctx, FeedbackAuthorParams{ChatID: chatID, ChatMessageID: int64(msgID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, docsbot.ErrFeedbackNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("feedback author: %w", err)
	}
	return tgUser, nil
}
