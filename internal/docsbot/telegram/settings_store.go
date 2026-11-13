package telegram

import (
	"sync"
	"time"
)

// settingsTTL is how long a /settings message is remembered for the OAuth
// return: the user may take a while on Google's consent screen.
const settingsTTL = 24 * time.Hour

// settingsStore remembers the latest /settings message of each chat, so that
// when Drive gets connected that very message can be turned into the connected
// screen. In memory only: after a restart or the TTL the message is simply not
// edited.
type settingsStore struct {
	mu  sync.Mutex
	m   map[int64]settingsEntry
	now func() time.Time
	// lastSweep is when expired entries were last dropped.
	lastSweep time.Time
}

// settingsSweepEvery is the minimum gap between sweeps of expired entries.
const settingsSweepEvery = time.Minute

type settingsEntry struct {
	msg int
	at  time.Time
}

func newSettingsStore(now func() time.Time) *settingsStore {
	return &settingsStore{m: map[int64]settingsEntry{}, now: now}
}

// set records msg as the chat's settings message. Expired entries are dropped at
// most once per settingsSweepEvery, so the map stays bounded by the chats active
// within the TTL.
func (s *settingsStore) set(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Sub(s.lastSweep) >= settingsSweepEvery {
		for c, e := range s.m {
			if now.Sub(e.at) > settingsTTL {
				delete(s.m, c)
			}
		}
		s.lastSweep = now
	}
	s.m[chat] = settingsEntry{msg: msg, at: now}
}

// forget drops the chat's entry if it is msg: that message stopped being the
// settings screen. A newer settings message is left alone.
func (s *settingsStore) forget(chat int64, msg int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[chat]; ok && e.msg == msg {
		delete(s.m, chat)
	}
}

// forgetChat drops the chat's entry whatever the message.
func (s *settingsStore) forgetChat(chat int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, chat)
}

// get returns the chat's settings message, if known and not expired.
func (s *settingsStore) get(chat int64) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[chat]
	if !ok {
		return 0, false
	}
	if s.now().Sub(e.at) > settingsTTL {
		delete(s.m, chat)
		return 0, false
	}
	return e.msg, true
}

// SettingsMessages is the settings-message memory shared by the docs handlers
// (which record the message) and NotifyConnected (which edits it).
type SettingsMessages struct{ store *settingsStore }

// NewSettingsMessages makes the shared memory; pass it to Register
// (WithSettingsMessages) and to NotifyConnected (WithConnectSettings).
func NewSettingsMessages() *SettingsMessages {
	return &SettingsMessages{store: newSettingsStore(time.Now)}
}

// WithSettingsMessages makes the docs handlers record their /settings messages
// in m.
func WithSettingsMessages(m *SettingsMessages) Option {
	return func(d *docs) { d.settingsMsgs = m.store }
}
