package telegram

import (
	"log/slog"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResultStore_take(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	s := newTokenMap[convResult](clock)
	s.put("tok", 1, convResult{suffix: "x"})

	_, ok := s.take("tok", 2)
	require.False(t, ok, "another user's token is refused")
	_, ok = s.take("missing", 1)
	require.False(t, ok)

	r, ok := s.take("tok", 1)
	require.True(t, ok, "foreign attempt must not burn the owner's token")
	require.Equal(t, "x", r.suffix)
	_, ok = s.take("tok", 1)
	require.False(t, ok, "double tap acts once")

	s.put("old", 1, convResult{})
	now = now.Add(sessionTTL + time.Second)
	_, ok = s.take("old", 1)
	require.False(t, ok, "expired token")
	_, ok = s.take("old", 1)
	require.False(t, ok)
}

func TestResultStore_putEvictsExpired(t *testing.T) {
	now := time.Now()
	s := newTokenMap[convResult](func() time.Time { return now })
	s.put("a", 1, convResult{})
	now = now.Add(sessionTTL + time.Minute)
	s.put("b", 1, convResult{})
	require.Len(t, s.m.m, 1)
}

func TestAlbumStore(t *testing.T) {
	now := time.Now()
	s := newTokenMap[convAlbum](func() time.Time { return now })
	s.put("t", 1, convAlbum{user: "u1", group: "g"})

	_, ok := s.peek("t", 2)
	require.False(t, ok, "foreign user can't peek")
	_, ok = s.take("t", 2)
	require.False(t, ok, "foreign user can't take")
	_, ok = s.peek("t", 1)
	require.True(t, ok)
	_, ok = s.take("t", 1)
	require.True(t, ok)
	_, ok = s.take("t", 1)
	require.False(t, ok, "double tap acts once")

	s.put("e", 1, convAlbum{user: "u1"})
	now = now.Add(sessionTTL + time.Second)
	_, ok = s.peek("e", 1)
	require.False(t, ok)
	_, ok = s.take("e", 1)
	require.False(t, ok)
}

func TestStores_concurrent(_ *testing.T) {
	results := newTokenMap[convResult](time.Now)
	albums := newTokenMap[convAlbum](time.Now)
	sessions := newSessionStore(time.Now)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := strconv.Itoa(i)
			results.put(tok, 1, convResult{})
			albums.put(tok, 1, convAlbum{user: "u"})
			sessions.start(int64(i % 5))
			sessions.update(int64(i%5), func(s *session) { s.images = append(s.images, pendingImage{msgID: i}) })
			sessions.has(int64(i % 5))
			albums.peek(tok, 1)
			results.take(tok, 1)
			albums.take(tok, 1)
			sessions.end(int64(i % 5))
		}()
	}
	wg.Wait()
}

func TestResultStore_singleWinnerOnConcurrentTake(t *testing.T) {
	s := newTokenMap[convResult](time.Now)
	s.put("t", 1, convResult{})

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := s.take("t", 1); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, wins)
}

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
			pendingImage{msgID: 3, fileID: "c"},
			pendingImage{msgID: 1, fileID: "a"},
			pendingImage{msgID: 2, fileID: "b"},
		)
	})

	sess, ok := store.live(1)
	require.True(t, ok)
	var ids []string
	for _, img := range sess.orderedImages() {
		ids = append(ids, img.fileID)
	}
	require.Equal(t, []string{"a", "b", "c"}, ids)
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
	c := newConverter(slog.New(slog.DiscardHandler), nil, nil, nil, nil)

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

// A photo session keeps file ids and sizes, never image bytes: a few users with
// twenty photos each must not be able to exhaust memory.
func TestPendingImage_holdsNoBytes(t *testing.T) {
	typ := reflect.TypeFor[pendingImage]()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		require.NotEqual(t, reflect.Slice, f.Type.Kind(), "field %s must not hold a slice (bytes)", f.Name)
	}
}

// Timed-out sessions of other users are dropped on any access, not only when
// their own owner returns.
func TestSessionStore_expiredSessionsAreSwept(t *testing.T) {
	now := time.Now()
	store := newSessionStore(func() time.Time { return now })
	store.start(1)
	store.start(2)
	store.update(1, func(s *session) { s.images = append(s.images, pendingImage{msgID: 1, fileID: "f"}) })

	now = now.Add(sessionTTL + time.Second)
	store.start(3)

	store.mu.Lock()
	defer store.mu.Unlock()
	require.Len(t, store.byUser, 1, "only the fresh session is left")
	require.Contains(t, store.byUser, int64(3))
}

func TestTokenMap_ownership(t *testing.T) {
	now := time.Now()
	s := newTokenMap[convResult](func() time.Time { return now })

	t.Run("a foreign peek and take leave the token to its owner", func(t *testing.T) {
		s.put("a", 1, convResult{suffix: "x"})
		_, ok := s.peek("a", 2)
		require.False(t, ok)
		_, ok = s.take("a", 2)
		require.False(t, ok)
		_, ok = s.take("a", 0)
		require.False(t, ok, "the zero owner owns nothing")
		r, ok := s.take("a", 1)
		require.True(t, ok)
		require.Equal(t, "x", r.suffix)
	})
	t.Run("peek does not consume", func(t *testing.T) {
		s.put("b", 1, convResult{})
		for range 3 {
			_, ok := s.peek("b", 1)
			require.True(t, ok)
		}
		_, ok := s.take("b", 1)
		require.True(t, ok)
		_, ok = s.peek("b", 1)
		require.False(t, ok, "taken")
	})
	t.Run("putting a token back renews its life and keeps one owner", func(t *testing.T) {
		s.put("c", 1, convResult{})
		now = now.Add(sessionTTL - time.Second)
		v, ok := s.take("c", 1)
		require.True(t, ok)
		s.put("c", 1, v) // a failed run gives the token back
		now = now.Add(sessionTTL - time.Second)
		_, ok = s.peek("c", 1)
		require.True(t, ok, "the retry got a fresh TTL")

		s.put("c", 2, convResult{}) // the same token under another owner replaces it
		_, ok = s.peek("c", 1)
		require.False(t, ok)
		_, ok = s.peek("c", 2)
		require.True(t, ok)
	})
	t.Run("an expired token is gone for the owner too", func(t *testing.T) {
		s.put("d", 1, convResult{})
		now = now.Add(sessionTTL + time.Second)
		_, ok := s.peek("d", 1)
		require.False(t, ok)
		_, ok = s.take("d", 1)
		require.False(t, ok)
	})
}

func TestTokenMap_foreignPressesNeverWinARace(t *testing.T) {
	s := newTokenMap[convAlbum](time.Now)
	s.put("t", 1, convAlbum{user: "u1"})

	var owner, foreign int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			who := int64(1 + i%2) // half the presses are somebody else's
			_, ok := s.take("t", who)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case ok && who == 1:
				owner++
			case ok:
				foreign++
			}
		}()
	}
	wg.Wait()

	require.Equal(t, 1, owner, "the owner's double tap acts once")
	require.Zero(t, foreign)
}

func TestTokenMap_isBoundedByTheCap(t *testing.T) {
	s := newTokenMap[convResult](time.Now)
	s.m.limit = 5

	for i := range 20 {
		s.put(strconv.Itoa(i), 1, convResult{})
	}

	require.Len(t, s.m.m, 5, "a flood of results does not pile up")
}
