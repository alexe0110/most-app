package telegram

import (
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// renameStore remembers which document a user is renaming and on which message
// the prompt sits. In memory only: a restart drops it, and a stale arm (older
// than the TTL) is ignored.
type renameStore struct {
	mu  sync.Mutex
	m   map[int64]renameEntry
	now func() time.Time
}

type renameEntry struct {
	id   docsbot.DocumentID
	chat int64 // the card's message: it shows the prompt, then the renamed card
	msg  int
	at   time.Time
}

const renameTTL = 5 * time.Minute

func newRenameStore(now func() time.Time) *renameStore {
	return &renameStore{m: map[int64]renameEntry{}, now: now}
}

func (s *renameStore) set(user int64, id docsbot.DocumentID, chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = renameEntry{id: id, chat: chat, msg: msg, at: s.now()}
}

// take removes and returns the armed rename, if any and not expired.
func (s *renameStore) take(user int64) (renameEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, found := s.m[user]
	if !found {
		return renameEntry{}, false
	}
	delete(s.m, user)
	if s.now().Sub(e.at) > renameTTL {
		return renameEntry{}, false
	}
	return e, true
}

// dropMessage forgets every rename armed on that message, whoever armed it. A
// message that is redrawn (a status message) no longer shows the prompt.
func (s *renameStore) dropMessage(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for user, e := range s.m {
		if e.chat == chat && e.msg == msg {
			delete(s.m, user)
		}
	}
}

// Renames is the rename state shared by the docs handlers and the Notifier: the
// worker redraws status messages, and a redraw ends a rename prompt shown on one.
type Renames struct{ store *renameStore }

// NewRenames makes the shared rename state; pass it to Register (WithRenames) and
// to NewNotifier (WithNotifierRenames).
func NewRenames() *Renames { return &Renames{store: newRenameStore(time.Now)} }

// drop forgets the user's rename if it is armed on that message; one armed on
// another message (a newer prompt) is left alone.
func (s *renameStore) drop(user int64, chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[user]; ok && e.chat == chat && e.msg == msg {
		delete(s.m, user)
	}
}

// originStore remembers where each card message was opened from (a list page, the
// search, or nowhere) so edits of the card keep its "Назад" and delete can return.
// In memory only; entries expire after originTTL.
type originStore struct {
	mu  sync.Mutex
	m   map[originKey]originEntry
	now func() time.Time
}

type originKey struct {
	chat int64
	msg  int
}

type originEntry struct {
	orig string // "l:<cat>:<page>", "s" or ""
	at   time.Time
}

const originTTL = 30 * time.Minute

func newOriginStore(now func() time.Time) *originStore {
	return &originStore{m: map[originKey]originEntry{}, now: now}
}

func (s *originStore) set(chat int64, msg int, orig string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[originKey{chat, msg}] = originEntry{orig: orig, at: s.now()}
}

// get returns the origin, or "" when absent or expired; the entry is kept.
func (s *originStore) get(chat int64, msg int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := originKey{chat, msg}
	e, ok := s.m[k]
	if !ok {
		return ""
	}
	if s.now().Sub(e.at) > originTTL {
		delete(s.m, k)
		return ""
	}
	return e.orig
}

// searchStore remembers each user's last search query so a card opened from the
// results can go back to them. In memory only; entries expire after searchTTL.
type searchStore struct {
	mu  sync.Mutex
	m   map[int64]searchEntry
	now func() time.Time
}

type searchEntry struct {
	query string
	at    time.Time
}

const searchTTL = 10 * time.Minute

func newSearchStore(now func() time.Time) *searchStore {
	return &searchStore{m: map[int64]searchEntry{}, now: now}
}

func (s *searchStore) set(user int64, query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = searchEntry{query: query, at: s.now()}
}

// take returns the remembered query if not expired. It keeps the entry so
// repeated "Назад" presses keep working.
func (s *searchStore) take(user int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[user]
	if !ok {
		return "", false
	}
	if s.now().Sub(e.at) > searchTTL {
		delete(s.m, user)
		return "", false
	}
	return e.query, true
}

// confirmStore remembers that a user is being asked to confirm data deletion. In
// memory only, like renameStore: a restart or a stale arm just means the user
// taps "Удалить мои данные" again.
type confirmStore struct {
	mu  sync.Mutex
	m   map[int64]time.Time
	now func() time.Time
}

const confirmTTL = 2 * time.Minute

func newConfirmStore(now func() time.Time) *confirmStore {
	return &confirmStore{m: map[int64]time.Time{}, now: now}
}

func (s *confirmStore) arm(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[user] = s.now()
}

// take reports whether the user has a live deletion confirmation armed, clearing it.
func (s *confirmStore) take(user int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.m[user]
	if !ok {
		return false
	}
	delete(s.m, user)
	return s.now().Sub(at) <= confirmTTL
}
