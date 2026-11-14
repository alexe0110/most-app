package telegram

import (
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// TTLs of the in-memory stores. All of them are memory only: a restart empties
// them, and each user of a store tolerates that (see its comment).
const (
	renameTTL   = 5 * time.Minute
	originTTL   = 30 * time.Minute
	searchTTL   = 10 * time.Minute
	confirmTTL  = 2 * time.Minute
	settingsTTL = 24 * time.Hour // the user may take a while on Google's consent screen
)

// renameEntry is a rename the user armed: the document, and the card message that
// shows the prompt and then the renamed card. A stale arm (older than renameTTL)
// is ignored.
type renameEntry struct {
	id   docsbot.DocumentID
	chat int64
	msg  int
}

// originKey is a card message; its origin is where the card was opened from (a
// list page "l:<cat>:<page>", the search "s", or nowhere ""), so edits of the card
// keep its "Назад". Losing it after originTTL only loses the way back.
type originKey struct {
	chat int64
	msg  int
}

// dropRenamesOn forgets every rename armed on that message, whoever armed it. A
// message that is redrawn (a status message) no longer shows the prompt.
func dropRenamesOn(m *ttlMap[int64, renameEntry], chat int64, msg int) {
	m.deleteFunc(func(_ int64, e renameEntry) bool { return e.chat == chat && e.msg == msg })
}

// dropRename forgets the user's rename if it is armed on that message; one armed
// on another message (a newer prompt) is left alone.
func dropRename(m *ttlMap[int64, renameEntry], user, chat int64, msg int) {
	m.deleteFunc(func(u int64, e renameEntry) bool { return u == user && e.chat == chat && e.msg == msg })
}

// forgetSettings drops the chat's settings message if it is msg: that message
// stopped being the settings screen. A newer settings message is left alone.
func forgetSettings(m *ttlMap[int64, int], chat int64, msg int) {
	m.deleteFunc(func(c int64, id int) bool { return c == chat && id == msg })
}

// Renames is the rename state shared by the docs handlers and the Notifier: the
// worker redraws status messages, and a redraw ends a rename prompt shown on one.
type Renames struct{ m *ttlMap[int64, renameEntry] }

// NewRenames makes the shared rename state; pass it to Register (WithRenames) and
// to NewNotifier (WithNotifierRenames).
func NewRenames() *Renames {
	return &Renames{m: newTTLMap[int64, renameEntry](renameTTL, maxStoreEntries, time.Now)}
}

// SettingsMessages is the settings-message memory shared by the docs handlers
// (which record the message) and NotifyConnected (which edits it).
type SettingsMessages struct{ m *ttlMap[int64, int] }

// NewSettingsMessages makes the shared memory; pass it to Register
// (WithSettingsMessages) and to NotifyConnected (WithConnectSettings).
func NewSettingsMessages() *SettingsMessages {
	return &SettingsMessages{m: newTTLMap[int64, int](settingsTTL, maxStoreEntries, time.Now)}
}

// WithSettingsMessages makes the docs handlers record their /settings messages
// in m.
func WithSettingsMessages(m *SettingsMessages) Option {
	return func(d *docs) { d.settingsMsgs = m.m }
}

// maxStoreEntries caps every ttlMap: a flood of distinct keys must not grow the
// memory without bound. The oldest entry makes room.
const maxStoreEntries = 10_000

// ttlMap is a mutex-guarded map whose entries expire ttl after they were set.
// Expired entries are dropped when a new one is set (no goroutine), so the size
// is bounded by the keys live within the TTL and by the cap. Memory only: a
// restart empties it, which each user of the map must tolerate.
type ttlMap[K comparable, V any] struct {
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

func newTTLMap[K comparable, V any](ttl time.Duration, limit int, now func() time.Time) *ttlMap[K, V] {
	return &ttlMap[K, V]{m: map[K]ttlEntry[V]{}, ttl: ttl, limit: limit, now: now}
}

// set stores v under k, renewing its TTL. It first drops the expired entries and,
// when a new key finds the map full, the oldest one.
func (t *ttlMap[K, V]) set(k K, v V) {
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

// get returns the live value; an expired one is dropped. The entry is kept.
func (t *ttlMap[K, V]) get(k K) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.getLocked(k)
}

// take removes and returns the live value, so a double tap acts once.
func (t *ttlMap[K, V]) take(k K) (V, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.getLocked(k)
	delete(t.m, k)
	return v, ok
}

// takeIf is take for a value the caller may refuse: a refused value stays, so a
// foreign attempt does not burn it.
func (t *ttlMap[K, V]) takeIf(k K, allow func(V) bool) (V, bool) {
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

func (t *ttlMap[K, V]) delete(k K) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, k)
}

// deleteFunc drops every entry for which drop reports true.
func (t *ttlMap[K, V]) deleteFunc(drop func(K, V) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.m {
		if drop(k, e.v) {
			delete(t.m, k)
		}
	}
}

func (t *ttlMap[K, V]) getLocked(k K) (V, bool) {
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

func (t *ttlMap[K, V]) expired(e ttlEntry[V], now time.Time) bool {
	return now.Sub(e.at) > t.ttl
}

func (t *ttlMap[K, V]) evictOldest() {
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

// origin is where the card message was opened from; "" when unknown or expired.
func (d *docs) origin(chat int64, msg int) string {
	orig, _ := d.origins.get(originKey{chat, msg})
	return orig
}
