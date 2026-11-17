package docsbot

// FeedbackRecord is one feedback request as it sits in the feedback chat: the
// header message and the copies of what the user wrote.
type FeedbackRecord struct {
	User       UserID
	ChatID     int64
	MessageIDs []int
}
