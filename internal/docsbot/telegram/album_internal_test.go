package telegram

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Many photos of one album arriving at once (run with -race): no add is lost
// and the album is flushed (the picker sent) once, not once per photo.
func TestAlbumBuffer_concurrentAddsAreNotLost(t *testing.T) {
	buf := newAlbumBuffer()
	buf.after = func(_ time.Duration, f func()) func() bool { return time.AfterFunc(50*time.Millisecond, f).Stop }

	const n = 200
	var fresh, flushes atomic.Int64
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf.add("u:alb", func(st *albumState) { st.fresh++ }, func(st albumState) {
				flushes.Add(1)
				fresh.Add(int64(st.fresh))
			})
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool { return fresh.Load() == n }, 3*time.Second, 10*time.Millisecond, "every photo must be flushed")
	time.Sleep(150 * time.Millisecond)
	require.EqualValues(t, n, fresh.Load(), "no photo is flushed twice")
	require.LessOrEqual(t, flushes.Load(), int64(3), "one picker for the album, not one per photo")

	buf.mu.Lock()
	defer buf.mu.Unlock()
	require.Empty(t, buf.m, "state is released after the flush")
}

func TestAlbumBuffer_differentAlbumsAreIndependent(t *testing.T) {
	buf := newAlbumBuffer()
	var timers []func()
	var mu sync.Mutex
	buf.after = func(_ time.Duration, f func()) func() bool {
		mu.Lock()
		defer mu.Unlock()
		timers = append(timers, f)
		return func() bool { return true }
	}
	got := map[string]int{}
	for _, k := range []string{"u1:a", "u2:a", "u1:b"} {
		buf.add(k, func(st *albumState) { st.fresh++ }, func(st albumState) { got[k] += st.fresh })
	}
	for _, f := range timers {
		f()
	}
	require.Equal(t, map[string]int{"u1:a": 1, "u2:a": 1, "u1:b": 1}, got)
}
