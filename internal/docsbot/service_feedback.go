package docsbot

import (
	"context"
	"fmt"
)

// CheckFeedback fails with ErrFeedbackLimit when the user has already made
// MaxFeedbackPerHour requests within the last hour. It runs before anything is
// sent to the feedback chat, so a refused request costs nothing there.
func (s *Service) CheckFeedback(ctx context.Context, user UserID) error {
	n, err := s.feedback.CountFeedbackSince(ctx, user, s.now().Add(-feedbackWindow))
	if err != nil {
		return fmt.Errorf("count feedback: %w", err)
	}
	if n >= MaxFeedbackPerHour {
		return ErrFeedbackLimit
	}
	return nil
}

// RecordFeedback stores the link between the messages the request left in the
// feedback chat and the user, then counts the request. The event carries only the
// source, never the text.
func (s *Service) RecordFeedback(ctx context.Context, user UserID, src FeedbackSource, chatID int64, ids []int) error {
	if err := s.feedback.RecordFeedback(ctx, FeedbackRecord{User: user, ChatID: chatID, MessageIDs: ids}); err != nil {
		return fmt.Errorf("record feedback: %w", err)
	}
	s.track(ctx, Event{User: user, Name: EventFeedbackSent, Props: EventProps{Source: string(src)}})
	return nil
}

// FeedbackAuthor returns the Telegram id of the user behind a message of the
// feedback chat, or ErrFeedbackNotFound.
func (s *Service) FeedbackAuthor(ctx context.Context, chatID int64, msgID int) (int64, error) {
	tgUser, err := s.feedback.FeedbackAuthor(ctx, chatID, msgID)
	if err != nil {
		return 0, fmt.Errorf("feedback author: %w", err)
	}
	return tgUser, nil
}
