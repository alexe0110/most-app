package telegram

import (
	"sort"
	"sync"
	"time"
)

// sessionTTL drops a session left idle this long. A restart drops all of them.
const sessionTTL = 15 * time.Minute

// step is the current stage of a converter dialogue.
type step int

const (
	stepChooseAction step = iota // waiting for Photo→PDF / Compress
	stepCollectPDF               // collecting photos for a PDF
	stepAwaitImage               // compress: waiting for the photo
	stepChooseLimit              // compress: waiting for the size
	stepCustomLimit              // compress: waiting for a size in KB
	stepChooseFormat             // compress: waiting for JPEG / PNG
)

// pendingImage is a downloaded image. msgID keeps album order (album photos
// arrive as separate updates).
type pendingImage struct {
	msgID int
	data  []byte
}

// session is one user's in-progress conversion, held only in memory.
type session struct {
	step     step
	images   []pendingImage
	maxBytes int
	updated  time.Time
}

// orderedImages returns the image bytes in arrival order.
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

// sessionStore holds sessions by Telegram user id. Safe for concurrent use:
// album photos arrive as parallel updates.
type sessionStore struct {
	mu     sync.Mutex
	byUser map[int64]*session
	ttl    time.Duration
	now    func() time.Time
}

func newSessionStore(now func() time.Time) *sessionStore {
	return &sessionStore{byUser: map[int64]*session{}, ttl: sessionTTL, now: now}
}

// live returns the user's session if it exists and has not timed out.
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

// start replaces any session with a fresh one.
func (s *sessionStore) start(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byUser[user] = &session{step: stepChooseAction, updated: s.now()}
}

// end removes the session and reports whether one was live.
func (s *sessionStore) end(user int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.liveLocked(user)
	delete(s.byUser, user)
	return ok
}

// update runs fn on the live session under the lock and refreshes its TTL, so
// parallel updates can't race. Reports whether a session was found.
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
