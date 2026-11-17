//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/postgres"
	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

const feedbackChat = int64(-100500)

func insertFeedbackAt(t *testing.T, pool *pgxpool.Pool, user docsbot.UserID, msgID int, age string) {
	t.Helper()
	_, err := pool.Exec(t.Context(),
		`insert into feedback_messages (feedback_id, user_id, chat_id, chat_message_id, created_at)
		 values (uuid_generate_v7(), $1, $2, $3, now() - $4::interval)`,
		string(user), feedbackChat, msgID, age)
	require.NoError(t, err)
}

func TestFeedback_authorByMessage(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	fb := postgres.NewFeedback(pool)
	u := makeUser(t, repo, 800)
	ctx := t.Context()

	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{
		User: docsbot.UserID(u.ID.String()), ChatID: feedbackChat, MessageIDs: []int{10, 11, 12},
	}))

	for _, id := range []int{10, 11, 12} {
		got, err := fb.FeedbackAuthor(ctx, feedbackChat, id)
		require.NoError(t, err)
		require.Equal(t, int64(800), got, "header and copies all lead to the author")
	}
	_, err := fb.FeedbackAuthor(ctx, feedbackChat, 99)
	require.ErrorIs(t, err, docsbot.ErrFeedbackNotFound)
	_, err = fb.FeedbackAuthor(ctx, -1, 10)
	require.ErrorIs(t, err, docsbot.ErrFeedbackNotFound, "the same id in another chat is a different message")
}

func TestFeedback_messageIDIsUniquePerChat(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	fb := postgres.NewFeedback(pool)
	a := docsbot.UserID(makeUser(t, repo, 801).ID.String())
	b := docsbot.UserID(makeUser(t, repo, 802).ID.String())
	ctx := t.Context()

	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: a, ChatID: feedbackChat, MessageIDs: []int{1}}))

	require.Error(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: b, ChatID: feedbackChat, MessageIDs: []int{1}}),
		"a taken message id cannot point at another user")
	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: b, ChatID: -7, MessageIDs: []int{1}}),
		"the same id in another chat is free")
}

func TestFeedback_countRequestsNotMessages(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	fb := postgres.NewFeedback(pool)
	user := docsbot.UserID(makeUser(t, repo, 803).ID.String())
	other := docsbot.UserID(makeUser(t, repo, 804).ID.String())
	ctx := t.Context()

	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: user, ChatID: feedbackChat, MessageIDs: []int{1, 2, 3, 4}}))
	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: user, ChatID: feedbackChat, MessageIDs: []int{5, 6}}))
	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: other, ChatID: feedbackChat, MessageIDs: []int{7, 8}}))
	insertFeedbackAt(t, pool, user, 9, "2 hours")

	got, err := fb.CountFeedbackSince(ctx, user, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, got, "an album with its header is one request; older and other users' ones are not counted")
}

func TestFeedback_softDeletedAuthorIsUnknown(t *testing.T) {
	pool := dbtest.New(t)
	repo := newAdapters(pool, noJobs{})
	fb := postgres.NewFeedback(pool)
	user := docsbot.UserID(makeUser(t, repo, 805).ID.String())
	ctx := t.Context()
	require.NoError(t, fb.RecordFeedback(ctx, docsbot.FeedbackRecord{User: user, ChatID: feedbackChat, MessageIDs: []int{1}}))

	_, err := pool.Exec(ctx, "update users set deleted_at = now() where id = $1", string(user))
	require.NoError(t, err)

	_, err = fb.FeedbackAuthor(ctx, feedbackChat, 1)
	require.ErrorIs(t, err, docsbot.ErrFeedbackNotFound)
}
