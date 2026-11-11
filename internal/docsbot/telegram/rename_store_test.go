package telegram

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestRenameStore(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s := newRenameStore(func() time.Time { return now })

	t.Run("take returns and removes", func(t *testing.T) {
		s.set(1, "doc", 100, 7)
		e, ok := s.take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("doc"), e.id)
		require.Equal(t, 7, e.msg)
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("ttl", func(t *testing.T) {
		s.set(1, "doc", 100, 7)
		now = now.Add(renameTTL)
		_, ok := s.take(1)
		require.True(t, ok, "exactly at the limit is still live")
		s.set(1, "doc", 100, 7)
		now = now.Add(renameTTL + time.Second)
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("drop only its own message", func(t *testing.T) {
		s.set(1, "doc", 100, 7)
		s.drop(1, 100, 8)
		_, ok := s.take(1)
		require.True(t, ok, "another message's cancel leaves it armed")
		s.set(1, "doc", 100, 7)
		s.drop(1, 100, 7)
		s.drop(1, 100, 7) // idempotent
		_, ok = s.take(1)
		require.False(t, ok)
	})
	t.Run("users are separate", func(t *testing.T) {
		s.set(1, "a", 100, 7)
		s.set(2, "b", 200, 8)
		s.drop(2, 200, 8)
		e, ok := s.take(1)
		require.True(t, ok)
		require.Equal(t, docsbot.DocumentID("a"), e.id)
	})
}

func TestRenameStore_concurrent(_ *testing.T) {
	s := newRenameStore(time.Now)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(3)
		go func() { defer wg.Done(); s.set(int64(i%5), "doc", 1, i) }()
		go func() { defer wg.Done(); s.take(int64(i % 5)) }()
		go func() { defer wg.Done(); s.drop(int64(i%5), 1, i) }()
	}
	wg.Wait()
}
