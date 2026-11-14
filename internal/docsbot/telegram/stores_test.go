package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
