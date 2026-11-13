package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSettingsStore_isolationBoundaryAndConcurrency(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	s := newSettingsStore(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })

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
	for i := 0; i < 50; i++ {
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
	s := newSettingsStore(func() time.Time { return now })

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
	s := newSettingsStore(time.Now)
	s.set(1, 10)

	s.forget(1, 99) // a different message: the entry stays
	_, ok := s.get(1)
	require.True(t, ok)

	s.forget(1, 10)
	_, ok = s.get(1)
	require.False(t, ok, "the message left the settings screen")

	s.set(2, 20)
	s.forgetChat(2)
	_, ok = s.get(2)
	require.False(t, ok)
}
