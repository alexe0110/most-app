package telegram

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionStore_timeout(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	store := newSessionStore(clock)

	store.start(7)
	require.True(t, store.has(7), "session is live right after it starts")

	now = now.Add(sessionTTL + time.Second)
	require.False(t, store.has(7), "session is gone once the TTL elapses")
	require.False(t, store.update(7, func(*session) {}), "a timed-out session can't be updated")
}

func TestSessionStore_orderedImages(t *testing.T) {
	store := newSessionStore(time.Now)
	store.start(1)

	// Append out of order; album photos may be processed concurrently.
	store.update(1, func(s *session) {
		s.images = append(s.images,
			pendingImage{msgID: 3, data: []byte("c")},
			pendingImage{msgID: 1, data: []byte("a")},
			pendingImage{msgID: 2, data: []byte("b")},
		)
	})

	sess, ok := store.live(1)
	require.True(t, ok)
	require.Equal(t, [][]byte{[]byte("a"), []byte("b"), []byte("c")}, sess.orderedImages())
}

func TestSessionStore_endReportsLiveness(t *testing.T) {
	store := newSessionStore(time.Now)

	require.False(t, store.end(5), "nothing to end")
	store.start(5)
	require.True(t, store.end(5), "ended a live session")
	require.False(t, store.has(5))
}
