package tg

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeUsers records the last Ensure call. It is the only implementation of the
// Users port in tests, which is why the interface exists (hard rule #3).
type fakeUsers struct {
	id      string
	err     error
	calls   int
	gotTgID int64
	gotLang string
}

func (f *fakeUsers) Ensure(_ context.Context, tgUserID int64, lang string) (string, error) {
	f.calls++
	f.gotTgID = tgUserID
	f.gotLang = lang
	return f.id, f.err
}

func messageUpdate(tgUserID int64, lang string) *models.Update {
	return &models.Update{
		Message: &models.Message{From: &models.User{ID: tgUserID, LanguageCode: lang}},
	}
}

func TestEnsureUser(t *testing.T) {
	t.Run("stashes id and forwards", func(t *testing.T) {
		users := &fakeUsers{id: "user-1"}
		var gotID string
		var gotOK, nextCalled bool
		h := ensureUser(users, discardLog())(func(ctx context.Context, _ *bot.Bot, _ *models.Update) {
			nextCalled = true
			gotID, gotOK = UserIDFromContext(ctx)
		})

		h(context.Background(), nil, messageUpdate(42, "en"))

		require.True(t, nextCalled)
		require.True(t, gotOK)
		require.Equal(t, "user-1", gotID)
		require.Equal(t, 1, users.calls)
		require.Equal(t, int64(42), users.gotTgID)
		require.Equal(t, "en", users.gotLang)
	})

	t.Run("no sender passes through without upsert", func(t *testing.T) {
		users := &fakeUsers{id: "user-1"}
		var nextCalled bool
		h := ensureUser(users, discardLog())(func(ctx context.Context, _ *bot.Bot, _ *models.Update) {
			nextCalled = true
			_, ok := UserIDFromContext(ctx)
			require.False(t, ok)
		})

		h(context.Background(), nil, &models.Update{})

		require.True(t, nextCalled)
		require.Zero(t, users.calls)
	})

	t.Run("storage error drops the update", func(t *testing.T) {
		users := &fakeUsers{err: errors.New("db down")}
		var nextCalled bool
		h := ensureUser(users, discardLog())(func(context.Context, *bot.Bot, *models.Update) {
			nextCalled = true
		})

		h(context.Background(), nil, messageUpdate(7, "ru"))

		require.False(t, nextCalled, "handler must not run when the user cannot be ensured")
		require.Equal(t, 1, users.calls)
	})
}
