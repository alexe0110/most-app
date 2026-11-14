package docsbottest

import (
	"sync"
	"time"
)

// FakeTimer is a manual after-func: nothing fires until the test calls Fire.
// Pass its After method where the code under test takes a timer function.
type FakeTimer struct {
	mu     sync.Mutex
	timers []*fakeTimerEntry
}

type fakeTimerEntry struct {
	f       func()
	stopped bool
}

// After records f and returns its stop function, which reports whether it
// stopped a timer that had not fired yet.
func (ft *FakeTimer) After(_ time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	e := &fakeTimerEntry{f: f}
	ft.timers = append(ft.timers, e)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !e.stopped
		e.stopped = true
		return was
	}
}

// Fire runs every timer that was not stopped, once.
func (ft *FakeTimer) Fire() {
	ft.mu.Lock()
	var run []func()
	for _, e := range ft.timers {
		if !e.stopped {
			e.stopped = true
			run = append(run, e.f)
		}
	}
	ft.mu.Unlock()
	for _, f := range run {
		f()
	}
}
