package telegram

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestRenames_takeExpireAndDrop(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[int64, renameEntry](renameTTL, maxStoreEntries, func() time.Time { return now })

	t.Run("take returns and removes", func(t *testing.T) {
		s.set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		e, ok := s.take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("doc"), e.id)
		require.Equal(t, 7, e.msg)
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("ttl", func(t *testing.T) {
		s.set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		now = now.Add(renameTTL)
		_, ok := s.take(1)
		require.True(t, ok, "exactly at the limit is still live")
		s.set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		now = now.Add(renameTTL + time.Second)
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("drop only its own message", func(t *testing.T) {
		s.set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		dropRename(s, 1, 100, 8)
		_, ok := s.take(1)
		require.True(t, ok, "another message's cancel leaves it armed")
		s.set(1, renameEntry{id: "doc", chat: 100, msg: 7})
		dropRename(s, 1, 100, 7)
		dropRename(s, 1, 100, 7) // idempotent
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("users are separate", func(t *testing.T) {
		s.set(1, renameEntry{id: "a", chat: 100, msg: 7})
		s.set(2, renameEntry{id: "b", chat: 200, msg: 8})
		dropRename(s, 2, 200, 8)
		e, ok := s.take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("a"), e.id)
	})
}

func TestRenames_concurrentUseIsSafe(_ *testing.T) {
	s := newTTLMap[int64, renameEntry](renameTTL, maxStoreEntries, time.Now)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(3)
		go func() { defer wg.Done(); s.set(int64(i%5), renameEntry{id: "doc", chat: 1, msg: i}) }()
		go func() { defer wg.Done(); s.take(int64(i % 5)) }()
		go func() { defer wg.Done(); dropRename(s, int64(i%5), 1, i) }()
	}
	wg.Wait()
}

func TestRenames_dropOnMessageForgetsEveryUser(t *testing.T) {
	s := newTTLMap[int64, renameEntry](renameTTL, maxStoreEntries, time.Now)
	s.set(1, renameEntry{id: "a", chat: 100, msg: 70})
	s.set(2, renameEntry{id: "b", chat: 100, msg: 70})
	s.set(3, renameEntry{id: "c", chat: 100, msg: 71})
	dropRenamesOn(s, 100, 70)
	_, ok := s.take(1)
	require.False(t, ok)
	_, ok = s.take(2)
	require.False(t, ok)
	_, ok = s.take(3)
	require.True(t, ok, "another message is untouched")
}

func TestSettingsMessages_chatsAreIsolatedAndConcurrentUseIsSafe(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := newTTLMap[int64, int](settingsTTL, maxStoreEntries, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })

	s.set(1, 10)
	s.set(2, 20)
	m1, _ := s.get(1)
	m2, _ := s.get(2)
	require.Equal(t, []int{10, 20}, []int{m1, m2}, "chats do not see each other")

	mu.Lock()
	now = now.Add(settingsTTL) // exactly the TTL: still valid
	mu.Unlock()
	_, ok := s.get(1)
	require.True(t, ok)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.set(int64(i%5), i)
			s.get(int64(i % 5))
		}()
	}
	wg.Wait()
}

func TestSettingsMessages_expireAndLatestWins(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[int64, int](settingsTTL, maxStoreEntries, func() time.Time { return now })

	_, ok := s.get(1)
	require.False(t, ok, "unknown chat")

	s.set(1, 10)
	s.set(2, 20)
	msg, ok := s.get(1)
	require.True(t, ok)
	require.Equal(t, 10, msg)

	s.set(1, 11)
	msg, _ = s.get(1)
	require.Equal(t, 11, msg, "the latest settings message wins")

	now = now.Add(settingsTTL + time.Minute)
	_, ok = s.get(1)
	require.False(t, ok, "expired")

	s.set(3, 30) // drops the expired chat 2
	require.Len(t, s.m, 1)
}

func TestSettingsMessages_forgetOnlyTheSameMessage(t *testing.T) {
	s := newTTLMap[int64, int](settingsTTL, maxStoreEntries, time.Now)
	s.set(1, 10)

	forgetSettings(s, 1, 99) // a different message: the entry stays
	_, ok := s.get(1)
	require.True(t, ok)

	forgetSettings(s, 1, 10)
	_, ok = s.get(1)
	require.False(t, ok, "the message left the settings screen")

	s.set(2, 20)
	s.delete(2)
	_, ok = s.get(2)
	require.False(t, ok)
}

func TestConfirms_armedOnceTakenOnceAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[int64, struct{}](confirmTTL, maxStoreEntries, func() time.Time { return now })

	s.set(1, struct{}{})
	_, ok := s.take(2)
	require.False(t, ok, "another user has nothing armed")
	_, ok = s.take(1)
	require.True(t, ok)
	_, ok = s.take(1)
	require.False(t, ok, "armed once, taken once")

	s.set(1, struct{}{})
	now = now.Add(confirmTTL + time.Second)
	_, ok = s.take(1)
	require.False(t, ok, "a stale arm is ignored")
}

func TestOrigins_expiredAreDroppedOnSet(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[originKey, string](originTTL, maxStoreEntries, func() time.Time { return now })

	for msg := range 100 {
		s.set(originKey{chat: 1, msg: msg}, "s") // cards that are never opened again
	}
	now = now.Add(originTTL + time.Second)
	s.set(originKey{chat: 1, msg: 1000}, "")

	require.Len(t, s.m, 1, "the unread expired origins do not pile up")
}

func TestOrigins_sizeIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	const limit = 200 // the real cap is maxStoreEntries; a small one keeps the test quick
	s := newTTLMap[originKey, string](originTTL, limit, func() time.Time { return now })

	for msg := range limit + 50 {
		s.set(originKey{chat: 1, msg: msg}, "s")
		now = now.Add(time.Millisecond)
	}

	require.Len(t, s.m, limit)
	_, ok := s.get(originKey{chat: 1, msg: 0})
	require.False(t, ok, "the oldest origin made room")
	_, ok = s.get(originKey{chat: 1, msg: limit + 49})
	require.True(t, ok)
}

func TestSearches_keepEntryAndDropExpiredOnSet(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[int64, string](searchTTL, maxStoreEntries, func() time.Time { return now })

	s.set(1, "паспорт")
	for range 2 {
		q, ok := s.get(1)
		require.True(t, ok, "repeated Назад keeps working")
		require.Equal(t, "паспорт", q)
	}

	s.set(2, "договор")
	now = now.Add(searchTTL + time.Second)
	s.set(3, "счёт")

	require.Len(t, s.m, 1, "users who never come back do not pile up")
	_, ok := s.get(3)
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
		{"renames", d.renames.ttl, d.renames.limit, renameTTL},
		{"confirms", d.confirms.ttl, d.confirms.limit, confirmTTL},
		{"searches", d.searches.ttl, d.searches.limit, searchTTL},
		{"origins", d.origins.ttl, d.origins.limit, originTTL},
		{"settings messages", d.settingsMsgs.ttl, d.settingsMsgs.limit, settingsTTL},
		{"shared renames", NewRenames().m.ttl, NewRenames().m.limit, renameTTL},
		{"shared settings messages", NewSettingsMessages().m.ttl, NewSettingsMessages().m.limit, settingsTTL},
		{"result tokens", newTokenMap[convResult](time.Now).m.ttl, newTokenMap[convResult](time.Now).m.limit, sessionTTL},
		{"album tokens", newTokenMap[convAlbum](time.Now).m.ttl, newTokenMap[convAlbum](time.Now).m.limit, sessionTTL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantTTL, tt.ttl)
			require.Equal(t, maxStoreEntries, tt.limit)
		})
	}
}

func TestOrigin_expiredIsReadAsNoOrigin(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	d := newDocs(nil, nil, slog.New(slog.DiscardHandler))
	d.origins = newTTLMap[originKey, string](originTTL, maxStoreEntries, func() time.Time { return now })

	d.origins.set(originKey{chat: 1, msg: 2}, "s")
	require.Equal(t, "s", d.origin(1, 2))
	require.Empty(t, d.origin(1, 3), "another message")
	require.Empty(t, d.origin(2, 2), "another chat")

	now = now.Add(originTTL + time.Second)
	require.Empty(t, d.origin(1, 2), "after the TTL the way back is gone")
}
