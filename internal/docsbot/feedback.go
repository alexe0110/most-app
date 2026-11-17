package docsbot

import "time"

// MaxFeedbackPerHour caps the feedback requests of one user per hour, so a stuck
// finger or a script cannot flood the team's chat. An album counts as one.
const MaxFeedbackPerHour = 5

const feedbackWindow = time.Hour

// FeedbackSource is where a feedback request started. Only these values reach the
// feedback_sent event.
type FeedbackSource string

// Feedback sources: the /feedback command, the /settings button and the
// «Сообщить о проблеме» button under an error.
const (
	FeedbackCommand  FeedbackSource = "command"
	FeedbackSettings FeedbackSource = "settings"
	FeedbackError    FeedbackSource = "error"
)

// FeedbackRecord is one feedback request as it sits in the feedback chat: the
// header message and the copies of what the user wrote.
type FeedbackRecord struct {
	User       UserID
	ChatID     int64
	MessageIDs []int
}
