package telegram

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestRenames_takeExpireAndDrop(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := tg.NewTTLMap[int64, renameEntry](renameTTL, tg.MaxStoreEntries, func() time.Time { return now })

	t.Run("take returns and removes", func(t *testing.T) {
		s.Set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		e, ok := s.Take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("doc"), e.id)
		require.Equal(t, 7, e.msg)
		_, ok = s.Take(1)
		require.False(t, ok)
	})
	t.Run("ttl", func(t *testing.T) {
		s.Set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		now = now.Add(renameTTL)
		_, ok := s.Take(1)
		require.True(t, ok, "exactly at the limit is still live")
		s.Set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		now = now.Add(renameTTL + time.Second)
		_, ok = s.Take(1)
		require.False(t, ok)
	})
	t.Run("drop only its own message", func(t *testing.T) {
		s.Set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		dropRename(s, 1, 100, 8)
		_, ok := s.Take(1)
		require.True(t, ok, "another message's cancel leaves it armed")
		s.Set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		dropRename(s, 1, 100, 7)
		dropRename(s, 1, 100, 7) // idempotent
		_, ok = s.Take(1)
		require.False(t, ok)
	})
	t.Run("users are separate", func(t *testing.T) {
		s.Set(1, renameEntry{id: "a", chat: 100, msg: 7})
		s.Set(2, renameEntry{id: "b", chat: 200, msg: 8})
		dropRename(s, 2, 200, 8)
		e, ok := s.Take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("a"), e.id)
	})
}

func TestRenames_concurrentUseIsSafe(_ *testing.T) {
	s := tg.NewTTLMap[int64, renameEntry](renameTTL, tg.MaxStoreEntries, time.Now)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(3)
		go func() { defer wg.Done(); s.Set(int64(i%5), renameEntry{id: "doc", chat: 1, msg: i}) }()
		go func() { defer wg.Done(); s.Take(int64(i % 5)) }()
		go func() { defer wg.Done(); dropRename(s, int64(i%5), 1, i) }()
	}
	wg.Wait()
}

func TestRenames_dropOnMessageForgetsEveryUser(t *testing.T) {
	s := tg.NewTTLMap[int64, renameEntry](renameTTL, tg.MaxStoreEntries, time.Now)
	s.Set(1, renameEntry{id: "a", chat: 100, msg: 70})
	s.Set(2, renameEntry{id: "b", chat: 100, msg: 70})
	s.Set(3, renameEntry{id: "c", chat: 100, msg: 71})
	dropRenamesOn(s, 100, 70)
	_, ok := s.Take(1)
	require.False(t, ok)
	_, ok = s.Take(2)
	require.False(t, ok)
	_, ok = s.Take(3)
	require.True(t, ok, "another message is untouched")
}

func TestSettingsMessages_chatsAreIsolatedAndConcurrentUseIsSafe(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := tg.NewTTLMap[int64, int](settingsTTL, tg.MaxStoreEntries, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })

	s.Set(1, 10)
	s.Set(2, 20)
	m1, _ := s.Get(1)
	m2, _ := s.Get(2)
	require.Equal(t, []int{10, 20}, []int{m1, m2}, "chats do not see each other")

	mu.Lock()
	now = now.Add(settingsTTL) // exactly the TTL: still valid
	mu.Unlock()
	_, ok := s.Get(1)
	require.True(t, ok)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Set(int64(i%5), i)
			s.Get(int64(i % 5))
		}()
	}
	wg.Wait()
}

func TestSettingsMessages_expireAndLatestWins(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s := tg.NewTTLMap[int64, int](settingsTTL, tg.MaxStoreEntries, func() time.Time { return now })

	_, ok := s.Get(1)
	require.False(t, ok, "unknown chat")

	s.Set(1, 10)
	s.Set(2, 20)
	msg, ok := s.Get(1)
	require.True(t, ok)
	require.Equal(t, 10, msg)

	s.Set(1, 11)
	msg, _ = s.Get(1)
	require.Equal(t, 11, msg, "the latest settings message wins")

	now = now.Add(settingsTTL + time.Minute)
	_, ok = s.Get(1)
	require.False(t, ok, "expired")

	s.Set(3, 30) // drops the expired chat 2
	require.Equal(t, 1, s.Len())
}

func TestSettingsMessages_forgetOnlyTheSameMessage(t *testing.T) {
	s := tg.NewTTLMap[int64, int](settingsTTL, tg.MaxStoreEntries, time.Now)
	s.Set(1, 10)

	forgetSettings(s, 1, 99) // a different message: the entry stays
	_, ok := s.Get(1)
	require.True(t, ok)

	forgetSettings(s, 1, 10)
	_, ok = s.Get(1)
	require.False(t, ok, "the message left the settings screen")

	s.Set(2, 20)
	s.Delete(2)
	_, ok = s.Get(2)
	require.False(t, ok)
}

func TestConfirms_armedOnceTakenOnceAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := tg.NewTTLMap[int64, struct{}](confirmTTL, tg.MaxStoreEntries, func() time.Time { return now })

	s.Set(1, struct{}{})
	_, ok := s.Take(2)
	require.False(t, ok, "another user has nothing armed")
	_, ok = s.Take(1)
	require.True(t, ok)
	_, ok = s.Take(1)
	require.False(t, ok, "armed once, taken once")

	s.Set(1, struct{}{})
	now = now.Add(confirmTTL + time.Second)
	_, ok = s.Take(1)
	require.False(t, ok, "a stale arm is ignored")
}

func TestOrigins_expiredAreDroppedOnSet(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := tg.NewTTLMap[originKey, string](originTTL, tg.MaxStoreEntries, func() time.Time { return now })

	for msg := range 100 {
		s.Set(originKey{chat: 1, msg: msg}, "s") // cards that are never opened again
	}
	now = now.Add(originTTL + time.Second)
	s.Set(originKey{chat: 1, msg: 1000}, "")

	require.Equal(t, 1, s.Len(), "the unread expired origins do not pile up")
}

func TestOrigins_sizeIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	const limit = 200 // the real cap is tg.MaxStoreEntries; a small one keeps the test quick
	s := tg.NewTTLMap[originKey, string](originTTL, limit, func() time.Time { return now })

	for msg := range limit + 50 {
		s.Set(originKey{chat: 1, msg: msg}, "s")
		now = now.Add(time.Millisecond)
	}

	require.Equal(t, limit, s.Len())
	_, ok := s.Get(originKey{chat: 1, msg: 0})
	require.False(t, ok, "the oldest origin made room")
	_, ok = s.Get(originKey{chat: 1, msg: limit + 49})
	require.True(t, ok)
}

func TestSearches_keepEntryAndDropExpiredOnSet(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := tg.NewTTLMap[int64, string](searchTTL, tg.MaxStoreEntries, func() time.Time { return now })

	s.Set(1, "паспорт")
	for range 2 {
		q, ok := s.Get(1)
		require.True(t, ok, "repeated Назад keeps working")
		require.Equal(t, "паспорт", q)
	}

	s.Set(2, "договор")
	now = now.Add(searchTTL + time.Second)
	s.Set(3, "счёт")

	require.Equal(t, 1, s.Len(), "users who never come back do not pile up")
	_, ok := s.Get(3)
	require.True(t, ok)
}

// The stores of the docs handlers and the shared Renames/SettingsMessages are
// the ones that leaked; this pins their TTL and cap to the constants.
func TestStores_areBoundedByTTLAndCap(t *testing.T) {
	d := newDocs(nil, nil, slog.New(slog.DiscardHandler))

	tests := []struct {
		name    string
		ttl     time.Duration
		limit   int
		wantTTL time.Duration
	}{
		{"renames", d.renames.TTL(), d.renames.Limit(), renameTTL},
		{"confirms", d.confirms.TTL(), d.confirms.Limit(), confirmTTL},
		{"searches", d.searches.TTL(), d.searches.Limit(), searchTTL},
		{"origins", d.origins.TTL(), d.origins.Limit(), originTTL},
		{"settings messages", d.settingsMsgs.TTL(), d.settingsMsgs.Limit(), settingsTTL},
		{"shared renames", NewRenames().m.TTL(), NewRenames().m.Limit(), renameTTL},
		{"shared settings messages", NewSettingsMessages().m.TTL(), NewSettingsMessages().m.Limit(), settingsTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantTTL, tt.ttl)
			require.Equal(t, tg.MaxStoreEntries, tt.limit)
		})
	}
}

func TestOrigin_expiredIsReadAsNoOrigin(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	d := newDocs(nil, nil, slog.New(slog.DiscardHandler))
	d.origins = tg.NewTTLMap[originKey, string](originTTL, tg.MaxStoreEntries, func() time.Time { return now })

	d.origins.Set(originKey{chat: 1, msg: 2}, "s")
	require.Equal(t, "s", d.origin(1, 2))
	require.Empty(t, d.origin(1, 3), "another message")
	require.Empty(t, d.origin(2, 2), "another chat")

	now = now.Add(originTTL + time.Second)
	require.Empty(t, d.origin(1, 2), "after the TTL the way back is gone")
}
