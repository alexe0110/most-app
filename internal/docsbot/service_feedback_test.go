package docsbot_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/docsbottest"
)

func TestService_CheckFeedback(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		recent  int
		old     int
		wantErr error
	}{
		{"none yet", 0, 0, nil},
		{"one below the limit", docsbot.MaxFeedbackPerHour - 1, 0, nil},
		{"at the limit", docsbot.MaxFeedbackPerHour, 0, docsbot.ErrFeedbackLimit},
		{"above the limit", docsbot.MaxFeedbackPerHour + 1, 0, docsbot.ErrFeedbackLimit},
		{"older than an hour do not count", 0, 10, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			for range tt.recent {
				repo.SeedFeedback(user, now.Add(-59*time.Minute))
			}
			for range tt.old {
				repo.SeedFeedback(user, now.Add(-61*time.Minute))
			}
			repo.SeedFeedback(stranger, now.Add(-time.Minute)) // another user's requests do not count
			svc := docsbottest.NewService(repo, docsbottest.FakeDrive{}, docsbottest.FakeFiles{},
				&docsbottest.FakeUploads{Repo: repo}, repo, docsbottest.NewFakeStatus(repo), &docsbottest.FakeSyncs{Repo: repo},
				docsbot.WithClock(func() time.Time { return now }))

			err := svc.CheckFeedback(t.Context(), user)

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestService_CheckFeedback_storageError(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.FeedbackErr = errors.New("db down")
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})

	err := svc.CheckFeedback(t.Context(), user)

	require.ErrorIs(t, err, repo.FeedbackErr)
	require.NotErrorIs(t, err, docsbot.ErrFeedbackLimit)
}

func TestService_RecordFeedback(t *testing.T) {
	tests := []struct {
		name      string
		src       docsbot.FeedbackSource
		storeErr  error
		wantEvent bool
	}{
		{"command", docsbot.FeedbackCommand, nil, true},
		{"settings", docsbot.FeedbackSettings, nil, true},
		{"error", docsbot.FeedbackError, nil, true},
		{"storage failure records no event", docsbot.FeedbackCommand, errors.New("db down"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := docsbottest.NewFakeRepo()
			repo.FeedbackErr = tt.storeErr
			svc, _ := edgeService(repo, docsbottest.FakeDrive{})

			err := svc.RecordFeedback(t.Context(), user, tt.src, -100, []int{4, 5, 6})

			if tt.storeErr != nil {
				require.ErrorIs(t, err, tt.storeErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, []docsbot.FeedbackRecord{{User: user, ChatID: -100, MessageIDs: []int{4, 5, 6}}},
					[]docsbot.FeedbackRecord{repo.Feedbacks[0].Record})
			}
			if !tt.wantEvent {
				require.Empty(t, repo.Events)
				return
			}
			require.Equal(t, []docsbot.Event{{
				User: user, Name: docsbot.EventFeedbackSent, Props: docsbot.EventProps{Source: string(tt.src)},
			}}, repo.Events, "the event carries the source and no text")
		})
	}
}

func TestService_FeedbackAuthor(t *testing.T) {
	repo := docsbottest.NewFakeRepo()
	repo.FeedbackTgUsers = map[docsbot.UserID]int64{user: 777}
	svc, _ := edgeService(repo, docsbottest.FakeDrive{})
	require.NoError(t, svc.RecordFeedback(t.Context(), user, docsbot.FeedbackCommand, -100, []int{4, 5}))

	got, err := svc.FeedbackAuthor(t.Context(), -100, 5)
	require.NoError(t, err)
	require.Equal(t, int64(777), got)

	_, err = svc.FeedbackAuthor(t.Context(), -100, 99)
	require.ErrorIs(t, err, docsbot.ErrFeedbackNotFound, "unknown message")
	_, err = svc.FeedbackAuthor(t.Context(), -200, 5)
	require.ErrorIs(t, err, docsbot.ErrFeedbackNotFound, "another chat")
}
