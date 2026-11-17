package docsbottest

import (
	"context"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

var _ docsbot.Feedback = (*FakeRepo)(nil)

// FeedbackEntry is one recorded feedback request.
type FeedbackEntry struct {
	Record docsbot.FeedbackRecord
	At     time.Time
}

// RecordFeedback stores the request, stamped with the current time, or fails with FeedbackErr.
func (r *FakeRepo) RecordFeedback(_ context.Context, rec docsbot.FeedbackRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.FeedbackErr != nil {
		return r.FeedbackErr
	}
	if r.RecordFeedbackErr != nil {
		return r.RecordFeedbackErr
	}
	r.Feedbacks = append(r.Feedbacks, FeedbackEntry{Record: rec, At: time.Now()})
	return nil
}

// SeedFeedback stores a request made at the given time, for limit tests.
func (r *FakeRepo) SeedFeedback(user docsbot.UserID, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Feedbacks = append(r.Feedbacks, FeedbackEntry{Record: docsbot.FeedbackRecord{User: user}, At: at})
}

// CountFeedbackSince counts the user's recorded requests made at or after since.
func (r *FakeRepo) CountFeedbackSince(_ context.Context, user docsbot.UserID, since time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.FeedbackErr != nil {
		return 0, r.FeedbackErr
	}
	n := 0
	for _, f := range r.Feedbacks {
		if f.Record.User == user && !f.At.Before(since) {
			n++
		}
	}
	return n, nil
}

// FeedbackAuthor finds the Telegram id of the user behind a recorded message;
// FeedbackTgUsers maps the user ids to the ids a test expects.
func (r *FakeRepo) FeedbackAuthor(_ context.Context, chatID int64, msgID int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.FeedbackErr != nil {
		return 0, r.FeedbackErr
	}
	for _, f := range r.Feedbacks {
		if f.Record.ChatID != chatID {
			continue
		}
		for _, id := range f.Record.MessageIDs {
			if id == msgID {
				if tg, ok := r.FeedbackTgUsers[f.Record.User]; ok {
					return tg, nil
				}
			}
		}
	}
	return 0, docsbot.ErrFeedbackNotFound
}
