package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestTTLMap_setGetDeleteTake(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := newTTLMap[int, string](time.Minute, 10, func() time.Time { return now })

	_, ok := m.get(1)
	require.False(t, ok, "unknown key")

	m.set(1, "a")
	v, ok := m.get(1)
	require.True(t, ok)
	require.Equal(t, "a", v)
	_, ok = m.get(1)
	require.True(t, ok, "get keeps the entry")

	m.set(1, "b")
	v, _ = m.get(1)
	require.Equal(t, "b", v, "the latest value wins")

	v, ok = m.take(1)
	require.True(t, ok)
	require.Equal(t, "b", v)
	_, ok = m.take(1)
	require.False(t, ok, "take removes")

	m.set(2, "c")
	m.delete(2)
	m.delete(2) // idempotent
	_, ok = m.get(2)
	require.False(t, ok)
}

func TestTTLMap_expiry(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := newTTLMap[int, string](time.Minute, 10, func() time.Time { return now })

	m.set(1, "a")
	now = now.Add(time.Minute)
	_, ok := m.get(1)
	require.True(t, ok, "exactly at the limit is still live")

	now = now.Add(time.Second)
	_, ok = m.get(1)
	require.False(t, ok, "expired")
	require.Zero(t, len(m.m), "get drops what it finds expired")

	m.set(2, "b")
	now = now.Add(time.Minute + time.Second)
	_, ok = m.take(2)
	require.False(t, ok, "take refuses an expired entry")
	require.Zero(t, len(m.m))
}

func TestTTLMap_setDropsExpired(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := newTTLMap[int, string](time.Minute, 10, func() time.Time { return now })

	m.set(1, "a")
	m.set(2, "b")
	now = now.Add(time.Minute + time.Second)
	m.set(3, "c")
	require.Len(t, m.m, 1, "only the new entry is left")
	_, ok := m.get(3)
	require.True(t, ok)
}

func TestTTLMap_sizeCap(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := newTTLMap[int, int](time.Hour, 3, func() time.Time { return now })

	for i := range 3 {
		m.set(i, i)
		now = now.Add(time.Second)
	}
	m.set(1, 11) // an existing key never evicts
	require.Len(t, m.m, 3)

	m.set(9, 9) // full: the oldest (key 0) makes room
	require.Len(t, m.m, 3)
	_, ok := m.get(0)
	require.False(t, ok, "the oldest was evicted")
	for _, k := range []int{1, 2, 9} {
		_, ok = m.get(k)
		require.True(t, ok, "key %d stays", k)
	}
}

func TestTTLMap_deleteFunc(t *testing.T) {
	m := newTTLMap[int, string](time.Hour, 10, time.Now)
	m.set(1, "a")
	m.set(2, "b")
	m.set(3, "a")

	m.deleteFunc(func(_ int, v string) bool { return v == "a" })
	require.Len(t, m.m, 1)
	_, ok := m.get(2)
	require.True(t, ok)
}

func TestTTLMap_concurrentTakeHasOneWinner(t *testing.T) {
	m := newTTLMap[string, int](time.Hour, 10, time.Now)
	m.set("t", 1)

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := m.take("t"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, wins)
}

func TestTTLMap_takeIf(t *testing.T) {
	m := newTTLMap[string, int](time.Hour, 10, time.Now)
	m.set("k", 7)

	_, ok := m.takeIf("k", func(v int) bool { return v == 8 })
	require.False(t, ok, "a refused value is not returned")
	v, ok := m.takeIf("k", func(v int) bool { return v == 7 })
	require.True(t, ok, "a refusal does not burn the entry")
	require.Equal(t, 7, v)
	_, ok = m.takeIf("k", func(int) bool { return true })
	require.False(t, ok, "taken once")
}

func TestRenameStore(t *testing.T) {
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

func TestRenameStore_concurrent(_ *testing.T) {
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

func TestRenameStore_dropMessage(t *testing.T) {
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

func TestSettingsStore_isolationBoundaryAndConcurrency(t *testing.T) {
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

func TestSettingsStore_ttl(t *testing.T) {
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

func TestSettingsStore_forget(t *testing.T) {
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

func TestConfirmStore_take(t *testing.T) {
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

func TestOriginStore_expiredAreDroppedOnSet(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newTTLMap[originKey, string](originTTL, maxStoreEntries, func() time.Time { return now })

	for msg := range 100 {
		s.set(originKey{chat: 1, msg: msg}, "s") // cards that are never opened again
	}
	now = now.Add(originTTL + time.Second)
	s.set(originKey{chat: 1, msg: 1000}, "")

	require.Len(t, s.m, 1, "the unread expired origins do not pile up")
}

func TestOriginStore_sizeIsCapped(t *testing.T) {
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

func TestSearchStore_keepsEntryAndDropsExpiredOnSet(t *testing.T) {
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
