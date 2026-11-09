package tg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeUsers records the last Ensure call.
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

func chatMessage(typ models.ChatType) *models.Update {
	return &models.Update{Message: &models.Message{
		From: &models.User{ID: 1},
		Chat: models.Chat{ID: -100, Type: typ},
	}}
}

func chatCallback(typ models.ChatType) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		From:    models.User{ID: 1},
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{Chat: models.Chat{ID: -100, Type: typ}}},
	}}
}

func TestPrivateOnly(t *testing.T) {
	tests := []struct {
		name      string
		update    *models.Update
		wantNext  bool
		wantLeave bool
	}{
		{"private message", chatMessage(models.ChatTypePrivate), true, false},
		{"private callback", chatCallback(models.ChatTypePrivate), true, false},
		{"group message", chatMessage(models.ChatTypeGroup), false, true},
		{"supergroup message", chatMessage(models.ChatTypeSupergroup), false, true},
		{"group callback", chatCallback(models.ChatTypeGroup), false, true},
		{"supergroup callback", chatCallback(models.ChatTypeSupergroup), false, true},
		{"channel message", chatMessage(models.ChatTypeChannel), false, false},
		{"channel post", &models.Update{ChannelPost: &models.Message{Chat: models.Chat{ID: -1, Type: models.ChatTypeChannel}}}, false, false},
		{"edited group message", &models.Update{EditedMessage: &models.Message{Chat: models.Chat{ID: -1, Type: models.ChatTypeGroup}}}, false, true},
		{"inaccessible group callback", &models.Update{CallbackQuery: &models.CallbackQuery{
			Message: models.MaybeInaccessibleMessage{
				Type:                models.MaybeInaccessibleMessageTypeInaccessibleMessage,
				InaccessibleMessage: &models.InaccessibleMessage{Chat: models.Chat{ID: -1, Type: models.ChatTypeSupergroup}},
			},
		}}, false, true},
		{"no chat at all", &models.Update{}, false, false},
		{"my_chat_member in group", &models.Update{MyChatMember: &models.ChatMemberUpdated{Chat: models.Chat{ID: -1, Type: models.ChatTypeGroup}}}, false, true},
		{"my_chat_member in supergroup", &models.Update{MyChatMember: &models.ChatMemberUpdated{Chat: models.Chat{ID: -1, Type: models.ChatTypeSupergroup}}}, false, true},
		{"my_chat_member in channel", &models.Update{MyChatMember: &models.ChatMemberUpdated{Chat: models.Chat{ID: -1, Type: models.ChatTypeChannel}}}, false, false},
		{"my_chat_member in private (user blocked bot)", &models.Update{MyChatMember: &models.ChatMemberUpdated{Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}}}, true, false},
		{"edited private message", &models.Update{EditedMessage: &models.Message{Chat: models.Chat{ID: 1, Type: models.ChatTypePrivate}}}, true, false},
		{"edited channel post", &models.Update{EditedChannelPost: &models.Message{Chat: models.Chat{ID: -1, Type: models.ChatTypeChannel}}}, false, false},
		{"callback without message", &models.Update{CallbackQuery: &models.CallbackQuery{From: models.User{ID: 1}}}, false, false},
		{"inline query", &models.Update{InlineQuery: &models.InlineQuery{From: &models.User{ID: 1}}}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nextCalled bool
			var left []int64
			leave := func(_ context.Context, _ *bot.Bot, chatID int64) { left = append(left, chatID) }
			h := privateOnly(leave, discardLog())(func(context.Context, *bot.Bot, *models.Update) { nextCalled = true })

			h(context.Background(), nil, tt.update)

			require.Equal(t, tt.wantNext, nextCalled)
			if tt.wantLeave {
				require.Len(t, left, 1, "one notice+leave per update")
			} else {
				require.Empty(t, left)
			}
		})
	}
}

func TestPrivateOnly_groupNeverCreatesUser(t *testing.T) {
	users := &fakeUsers{id: "user-1"}
	leave := func(context.Context, *bot.Bot, int64) {}
	var reached bool
	chain := privateOnly(leave, discardLog())(ensureUser(users, discardLog())(func(context.Context, *bot.Bot, *models.Update) { reached = true }))

	chain(context.Background(), nil, chatMessage(models.ChatTypeSupergroup))

	require.False(t, reached)
	require.Zero(t, users.calls, "privateOnly must run before ensureUser")
}

func TestPrivateOnly_nonPrivateNeverCreatesUser(t *testing.T) {
	updates := map[string]*models.Update{
		"group callback":      chatCallback(models.ChatTypeGroup),
		"my_chat_member":      {MyChatMember: &models.ChatMemberUpdated{Chat: models.Chat{ID: -1, Type: models.ChatTypeGroup}}},
		"edited group":        {EditedMessage: &models.Message{From: &models.User{ID: 1}, Chat: models.Chat{ID: -1, Type: models.ChatTypeGroup}}},
		"channel post":        {ChannelPost: &models.Message{Chat: models.Chat{ID: -1, Type: models.ChatTypeChannel}}},
		"unknown chat update": {},
	}
	for name, u := range updates {
		t.Run(name, func(t *testing.T) {
			users := &fakeUsers{id: "user-1"}
			var reached bool
			chain := privateOnly(func(context.Context, *bot.Bot, int64) {}, discardLog())(
				ensureUser(users, discardLog())(func(context.Context, *bot.Bot, *models.Update) { reached = true }))

			chain(context.Background(), nil, u)

			require.False(t, reached)
			require.Zero(t, users.calls)
		})
	}
}

func TestLeaveGroup_sendsNoticeThenLeaves(t *testing.T) {
	var calls []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(ts.Close)
	b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe())
	require.NoError(t, err)

	leaveGroup("only private", discardLog())(context.Background(), b, -100)

	require.Equal(t, []string{"sendMessage", "leaveChat"}, calls)
}
