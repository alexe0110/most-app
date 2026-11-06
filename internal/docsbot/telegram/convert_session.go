package telegram

import (
	"sort"
	"sync"
	"time"
)

// sessionTTL drops a converter session left idle for this long; a restart drops
// all of them. Both are acceptable for a single instance (issue #39).
const sessionTTL = 15 * time.Minute

// step is where a converter session is in its dialogue.
type step int

const (
	stepChooseAction step = iota // waiting for the Photo→PDF / Compress choice
	stepCollectPDF               // collecting photos for a PDF
	stepAwaitImage               // compress: waiting for the single photo
	stepChooseLimit              // compress: waiting for the size choice
	stepCustomLimit              // compress: waiting for a size typed in KB
	stepChooseFormat             // compress: waiting for the JPEG / PNG choice
)

// pendingImage is a downloaded image kept until the session completes. msgID
// records arrival order so album photos (separate updates sharing a
// media_group_id) end up in the order the user sent them.
type pendingImage struct {
	msgID int
	data  []byte
}

// session is one user's in-progress converter interaction, held only in memory.
type session struct {
	step     step
	images   []pendingImage
	maxBytes int
	updated  time.Time
}

// orderedImages returns the collected image bytes in arrival order.
func (s *session) orderedImages() [][]byte {
	sorted := make([]pendingImage, len(s.images))
	copy(sorted, s.images)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].msgID < sorted[j].msgID })

	out := make([][]byte, len(sorted))
	for i, img := range sorted {
		out[i] = img.data
	}
	return out
}

// sessionStore keeps converter sessions keyed by Telegram user id. It is safe
// for concurrent use: the messages of one album arrive as separate updates that
// the bot processes in parallel.
type sessionStore struct {
	mu     sync.Mutex
	byUser map[int64]*session
	ttl    time.Duration
	now    func() time.Time
}

func newSessionStore(now func() time.Time) *sessionStore {
	return &sessionStore{byUser: map[int64]*session{}, ttl: sessionTTL, now: now}
}

// live returns the user's session if it exists and has not timed out, dropping
// it if it has.
func (s *sessionStore) live(user int64) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveLocked(user)
}

func (s *sessionStore) liveLocked(user int64) (*session, bool) {
	sess, ok := s.byUser[user]
	if !ok {
		return nil, false
	}
	if s.now().Sub(sess.updated) > s.ttl {
		delete(s.byUser, user)
		return nil, false
	}
	return sess, true
}

// has reports whether the user has a live session.
func (s *sessionStore) has(user int64) bool {
	_, ok := s.live(user)
	return ok
}

// start replaces any existing session with a fresh one at stepChooseAction.
func (s *sessionStore) start(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byUser[user] = &session{step: stepChooseAction, updated: s.now()}
}

// end removes the user's session, returning whether one was live.
func (s *sessionStore) end(user int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.liveLocked(user)
	delete(s.byUser, user)
	return ok
}

// update runs fn against the user's live session under the lock and refreshes
// its TTL, so concurrent updates (an album's photos) can't race. It reports
// whether a live session was found.
func (s *sessionStore) update(user int64, fn func(*session)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.liveLocked(user)
	if !ok {
		return false
	}
	fn(sess)
	sess.updated = s.now()
	return true
}
