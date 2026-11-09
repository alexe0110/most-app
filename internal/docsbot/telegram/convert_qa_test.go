package telegram

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestResultStore_take(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	s := newResultStore(clock)
	s.put("tok", convResult{tgUser: 1, suffix: "x"})

	_, ok := s.take("tok", 2)
	require.False(t, ok, "another user's token is refused")
	_, ok = s.take("missing", 1)
	require.False(t, ok)

	r, ok := s.take("tok", 1)
	require.True(t, ok, "foreign attempt must not burn the owner's token")
	require.Equal(t, "x", r.suffix)
	_, ok = s.take("tok", 1)
	require.False(t, ok, "double tap acts once")

	s.put("old", convResult{tgUser: 1})
	now = now.Add(sessionTTL + time.Second)
	_, ok = s.take("old", 1)
	require.False(t, ok, "expired token")
	_, ok = s.take("old", 1)
	require.False(t, ok)
}

func TestResultStore_putEvictsExpired(t *testing.T) {
	now := time.Now()
	s := newResultStore(func() time.Time { return now })
	s.put("a", convResult{tgUser: 1})
	now = now.Add(sessionTTL + time.Minute)
	s.put("b", convResult{tgUser: 1})
	require.Len(t, s.byToken, 1)
}

func TestAlbumStore(t *testing.T) {
	now := time.Now()
	s := newAlbumStore(func() time.Time { return now })
	s.put("t", convAlbum{user: "u1", group: "g"})

	_, ok := s.peek("t", "u2")
	require.False(t, ok, "foreign user can't peek")
	_, ok = s.take("t", "u2")
	require.False(t, ok, "foreign user can't take")
	_, ok = s.peek("t", "u1")
	require.True(t, ok)
	_, ok = s.take("t", "u1")
	require.True(t, ok)
	_, ok = s.take("t", "u1")
	require.False(t, ok, "double tap acts once")

	s.put("e", convAlbum{user: "u1"})
	now = now.Add(sessionTTL + time.Second)
	_, ok = s.peek("e", "u1")
	require.False(t, ok)
	_, ok = s.take("e", "u1")
	require.False(t, ok)
}

func TestStores_concurrent(t *testing.T) {
	results := newResultStore(time.Now)
	albums := newAlbumStore(time.Now)
	sessions := newSessionStore(time.Now)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok := strconv.Itoa(i)
			results.put(tok, convResult{tgUser: 1})
			albums.put(tok, convAlbum{user: "u"})
			sessions.start(int64(i % 5))
			sessions.update(int64(i%5), func(s *session) { s.images = append(s.images, pendingImage{msgID: i}) })
			sessions.has(int64(i % 5))
			albums.peek(tok, "u")
			results.take(tok, 1)
			albums.take(tok, "u")
			sessions.end(int64(i % 5))
		}()
	}
	wg.Wait()
}

func TestResultStore_singleWinnerOnConcurrentTake(t *testing.T) {
	s := newResultStore(time.Now)
	s.put("t", convResult{tgUser: 1})

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

func TestCallbackDataFitsTelegramLimit(t *testing.T) {
	// A UUID doc id is the longest id we use (36 bytes).
	id := docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	token := newToken()

	var data []string
	for _, m := range quickKeyboard(id, true).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	for _, m := range pdfMenuKeyboard(id).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	for _, m := range saveKeyboard(token).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	data = append(data, cbAlbumPrefix+token, cbDocPrefix+string(id))

	for _, d := range data {
		require.LessOrEqual(t, len(d), 64, d)
		require.NotEmpty(t, d)
	}
}
