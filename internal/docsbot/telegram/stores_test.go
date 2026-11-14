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

func TestTTLMap_takeOnUnknownKeyChangesNothing(t *testing.T) {
	m := newTTLMap[int, string](time.Hour, 10, time.Now)
	m.set(1, "a")

	v, ok := m.take(2)

	require.False(t, ok)
	require.Empty(t, v)
	require.Len(t, m.m, 1)
}

func TestTTLMap_setRenewsTheTTL(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := newTTLMap[int, string](time.Minute, 10, func() time.Time { return now })

	m.set(1, "a")
	now = now.Add(40 * time.Second)
	m.set(1, "b")
	now = now.Add(40 * time.Second) // 80s after the first set, 40s after the second

	v, ok := m.get(1)
	require.True(t, ok, "the second set started a new lifetime")
	require.Equal(t, "b", v)
}

func TestTTLMap_deleteFuncSeesOnlyStoredValues(t *testing.T) {
	m := newTTLMap[int, int](time.Hour, 10, time.Now)
	m.deleteFunc(func(int, int) bool { return true }) // on an empty map
	m.set(1, 10)
	m.set(2, 20)

	var seen []int
	m.deleteFunc(func(k, v int) bool {
		seen = append(seen, v)
		return k == 1
	})

	require.ElementsMatch(t, []int{10, 20}, seen)
	require.Len(t, m.m, 1)
}

func TestTTLMap_concurrentMixedOperations(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	const limit = 20
	m := newTTLMap[int, int](time.Second, limit, clock)

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := i % 30
			m.set(k, i)
			m.get(k)
			m.takeIf(k, func(v int) bool { return v%2 == 0 })
			m.take(k + 1)
			m.deleteFunc(func(_ int, v int) bool { return v == i })
			m.delete(k + 2)
			if i%50 == 0 {
				mu.Lock()
				now = now.Add(2 * time.Second) // expire entries while others run
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.LessOrEqual(t, len(m.m), limit)
}

func TestTTLMap_concurrentSetsStayWithinTheCap(t *testing.T) {
	const limit = 50
	m := newTTLMap[int, int](time.Hour, limit, time.Now)

	var wg sync.WaitGroup
	for i := range 500 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.set(i, i)
		}()
	}
	wg.Wait()

	require.Len(t, m.m, limit)
}

func TestTTLMap_sizeCap(t *testing.T) {
	type set struct {
		key   int
		after time.Duration // how far the clock moves once the key is set
	}
	const sec = time.Second
	tests := []struct {
		name     string
		ttl      time.Duration
		limit    int
		sets     []set
		wantLen  int
		wantKeys []int
		goneKeys []int
	}{
		{"the oldest makes room, an existing key never evicts", time.Hour, 3,
			[]set{{0, sec}, {1, sec}, {2, sec}, {1, 0}, {9, 0}}, 3, []int{1, 2, 9}, []int{0}},
		{"a renewed key is not the oldest", time.Hour, 2,
			[]set{{1, sec}, {2, sec}, {1, 0}, {3, 0}}, 2, []int{1, 3}, []int{2}},
		{"expired entries make room before a live one is evicted", time.Minute, 2,
			[]set{{1, time.Minute + sec}, {2, 0}, {3, 0}}, 2, []int{2, 3}, []int{1}},
		{"capacity of one keeps the newest", time.Hour, 1,
			[]set{{0, 0}, {1, 0}, {2, 0}, {3, 0}, {4, 0}}, 1, []int{4}, []int{0, 3}},
		{"equal timestamps stay bounded and keep the new key", time.Hour, 3,
			func() []set {
				var s []set
				for i := range 50 {
					s = append(s, set{i, 0})
				}
				return s
			}(), 3, []int{49}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			m := newTTLMap[int, int](tt.ttl, tt.limit, func() time.Time { return now })
			for _, s := range tt.sets {
				m.set(s.key, s.key)
				_, ok := m.get(s.key)
				require.True(t, ok, "the key just set is never the one evicted")
				require.LessOrEqual(t, len(m.m), tt.limit)
				now = now.Add(s.after)
			}
			require.Len(t, m.m, tt.wantLen)
			for _, k := range tt.wantKeys {
				_, ok := m.get(k)
				require.True(t, ok, "key %d stays", k)
			}
			for _, k := range tt.goneKeys {
				_, ok := m.get(k)
				require.False(t, ok, "key %d was evicted", k)
			}
		})
	}
}

func TestTTLMap_takeIf(t *testing.T) {
	tests := []struct {
		name      string
		set       bool          // whether "k" holds 7
		age       time.Duration // how long after the set the take happens
		allow     func(int) bool
		wantOK    bool
		wantAfter bool // whether the entry can still be taken afterwards
	}{
		{"a refused value is not returned and not burnt", true, 0, func(v int) bool { return v == 8 }, false, true},
		{"an allowed value is taken once", true, 0, func(v int) bool { return v == 7 }, true, false},
		{"unknown key", false, 0, func(int) bool { return true }, false, false},
		{"an expired entry is refused even for the owner and dropped", true, time.Minute + time.Second,
			func(int) bool { return true }, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
			m := newTTLMap[string, int](time.Minute, 10, func() time.Time { return now })
			if tt.set {
				m.set("k", 7)
			}
			now = now.Add(tt.age)

			v, ok := m.takeIf("k", tt.allow)

			require.Equal(t, tt.wantOK, ok)
			if ok {
				require.Equal(t, 7, v)
			}
			_, again := m.takeIf("k", func(int) bool { return true })
			require.Equal(t, tt.wantAfter, again)
		})
	}
}
