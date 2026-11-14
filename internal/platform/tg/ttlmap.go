package tg

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// MaxStoreEntries caps every TTLMap: a flood of distinct keys must not grow the
// memory without bound. The oldest entry makes room.
const MaxStoreEntries = 10_000

// TTLMap is a mutex-guarded map whose entries expire ttl after they were set.
// Expired entries are dropped when a new one is set (no goroutine), so the size
// is bounded by the keys live within the TTL and by the cap. Memory only: a
// restart empties it, which each user of the map must tolerate.
type TTLMap[K comparable, V any] struct {
	mu    sync.Mutex
	m     map[K]ttlEntry[V]
	ttl   time.Duration
	limit int
	now   func() time.Time
}

type ttlEntry[V any] struct {
	v  V
	at time.Time
}

// NewTTLMap makes a map whose entries live ttl, holding at most limit; now is the clock.
func NewTTLMap[K comparable, V any](ttl time.Duration, limit int, now func() time.Time) *TTLMap[K, V] {
	return &TTLMap[K, V]{m: map[K]ttlEntry[V]{}, ttl: ttl, limit: limit, now: now}
}

// Set stores v under k, renewing its TTL. It first drops the expired entries and,
// when a new key finds the map full, the oldest one.
func (t *TTLMap[K, V]) Set(k K, v V) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for key, e := range t.m {
		if t.expired(e, now) {
			delete(t.m, key)
		}
	}
	if _, exists := t.m[k]; !exists && len(t.m) >= t.limit {
		t.evictOldest()
	}
	t.m[k] = ttlEntry[V]{v: v, at: now}
}

// Get returns the live value; an expired one is dropped. The entry is kept.
func (t *TTLMap[K, V]) Get(k K) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.getLocked(k)
}

// Take removes and returns the live value, so a double tap acts once.
func (t *TTLMap[K, V]) Take(k K) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.getLocked(k)
	delete(t.m, k)
	return v, ok
}

// TakeIf is Take for a value the caller may refuse: a refused value stays, so a
// foreign attempt does not burn it.
func (t *TTLMap[K, V]) TakeIf(k K, allow func(V) bool) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.getLocked(k)
	if !ok || !allow(v) {
		var zero V
		return zero, false
	}
	delete(t.m, k)
	return v, true
}

// Delete drops the entry of k.
func (t *TTLMap[K, V]) Delete(k K) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, k)
}

// DeleteFunc drops every entry for which drop reports true.
func (t *TTLMap[K, V]) DeleteFunc(drop func(K, V) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if drop(k, e.v) {
			delete(t.m, k)
		}
	}
}

func (t *TTLMap[K, V]) getLocked(k K) (V, bool) {
	e, ok := t.m[k]
	if !ok {
		var zero V
		return zero, false
	}
	if t.expired(e, t.now()) {
		delete(t.m, k)
		var zero V
		return zero, false
	}
	return e.v, true
}

func (t *TTLMap[K, V]) expired(e ttlEntry[V], now time.Time) bool {
	return now.Sub(e.at) > t.ttl
}

func (t *TTLMap[K, V]) evictOldest() {
	var oldest K
	var oldestAt time.Time
	found := false
	for k, e := range t.m {
		if !found || e.at.Before(oldestAt) {
			oldest, oldestAt, found = k, e.at, true
		}
	}
	if found {
		delete(t.m, oldest)
	}
}

// Len is the number of stored entries, expired ones not yet dropped included.
func (t *TTLMap[K, V]) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.m)
}

// TTL is how long an entry lives after it was set.
func (t *TTLMap[K, V]) TTL() time.Duration { return t.ttl }

// Limit is the largest number of entries the map holds.
func (t *TTLMap[K, V]) Limit() int { return t.limit }

// NewToken returns a random 16-character token for the data of an inline button
// (a file id would not fit its 64 bytes, and must not be logged).
func NewToken() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails on supported platforms
	return hex.EncodeToString(raw[:])
}
