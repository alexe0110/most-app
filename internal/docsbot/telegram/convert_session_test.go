package telegram

import (
	"log/slog"
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

func TestSessionStore_pressValidation(t *testing.T) {
	now := time.Now()
	store := newSessionStore(func() time.Time { return now })
	store.start(1)
	store.setMsg(1, 50)

	tests := []struct {
		name  string
		msgID int
		steps []step
		want  bool
	}{
		{"right message and step", 50, []step{stepChooseAction}, true},
		{"any step", 50, nil, true},
		{"unknown message id", 0, []step{stepChooseAction}, true},
		{"other message", 51, []step{stepChooseAction}, false},
		{"other step", 50, []step{stepChooseFormat}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := store.check(1, tt.msgID, tt.steps...)
			require.Equal(t, tt.want, ok)
		})
	}
	_, ok := store.check(2, 50)
	require.False(t, ok, "no session")
}

// A session whose dialogue message is unknown (the send failed) has no current
// button: a press on an old message must not slip through.
func TestSessionStore_unknownDialogueMessageRejectsPresses(t *testing.T) {
	store := newSessionStore(time.Now)
	store.start(1)

	_, ok := store.check(1, 7, stepChooseAction)
	require.False(t, ok, "check")
	_, ok = store.advance(1, 7, []step{stepChooseAction}, func(*session) {})
	require.False(t, ok, "advance")
	_, ok = store.claim(1, 7)
	require.False(t, ok, "claim")
	_, ok = store.claim(1, 0, stepChooseAction)
	require.True(t, ok, "typed message (id 0) still works")
}

func TestConverter_claimMenuOnce(t *testing.T) {
	c := newConverter(slog.New(slog.DiscardHandler), nil, nil, nil)

	release, ok := c.claimMenu(1, 10)
	require.True(t, ok)
	_, again := c.claimMenu(1, 10)
	require.False(t, again, "a second tap on the same menu is refused while it runs")
	_, other := c.claimMenu(1, 11)
	require.True(t, other, "another message is independent")

	release()
	_, afterRelease := c.claimMenu(1, 10)
	require.True(t, afterRelease)
}
