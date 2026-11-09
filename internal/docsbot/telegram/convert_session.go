package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
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

// convResult is a converter output waiting for the user's "save or just the
// file" choice. The file is already in Telegram: file holds the ids Telegram
// returned for it, so saving needs no upload.
type convResult struct {
	tgUser    int64
	user      docsbot.UserID
	file      docsbot.IncomingFile
	sourceDoc docsbot.DocumentID // "" when the source is not a stored document
	suffix    string             // title suffix for the saved copy
	created   time.Time
}

// resultStore holds results by a short token that rides in the button's
// callback data (a file id would not fit, and must not be logged). Memory only:
// a restart or the TTL drops the choice, and the file stays in the chat.
type resultStore struct {
	mu      sync.Mutex
	byToken map[string]convResult
	ttl     time.Duration
	now     func() time.Time
}

func newResultStore(now func() time.Time) *resultStore {
	return &resultStore{byToken: map[string]convResult{}, ttl: sessionTTL, now: now}
}

// newToken returns a random 16-character token.
func newToken() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails on supported platforms
	return hex.EncodeToString(raw[:])
}

// put stores a result under token and drops expired ones.
func (s *resultStore) put(token string, r convResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, v := range s.byToken {
		if now.Sub(v.created) > s.ttl {
			delete(s.byToken, k)
		}
	}
	r.created = now
	s.byToken[token] = r
}

// take removes and returns the user's result, so a double tap acts once. A token
// of another user, an unknown or an expired one reports false.
func (s *resultStore) take(token string, tgUser int64) (convResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byToken[token]
	if !ok || r.tgUser != tgUser {
		return convResult{}, false
	}
	delete(s.byToken, token)
	if s.now().Sub(r.created) > s.ttl {
		return convResult{}, false
	}
	return r, true
}

// convAlbum is an album of images offered as one PDF.
type convAlbum struct {
	user    docsbot.UserID
	group   string
	images  []albumImage // ordered by message id
	created time.Time
}

// albumStore holds offered albums by token, like resultStore.
type albumStore struct {
	mu      sync.Mutex
	byToken map[string]convAlbum
	ttl     time.Duration
	now     func() time.Time
}

func newAlbumStore(now func() time.Time) *albumStore {
	return &albumStore{byToken: map[string]convAlbum{}, ttl: sessionTTL, now: now}
}

// put stores an album under token and drops expired ones.
func (s *albumStore) put(token string, a convAlbum) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, v := range s.byToken {
		if now.Sub(v.created) > s.ttl {
			delete(s.byToken, k)
		}
	}
	if a.created.IsZero() {
		a.created = now
	}
	s.byToken[token] = a
}

// take removes and returns the user's album, so a double tap acts once.
func (s *albumStore) take(token string, user docsbot.UserID) (convAlbum, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byToken[token]
	if !ok || a.user != user || s.now().Sub(a.created) > s.ttl {
		return convAlbum{}, false
	}
	delete(s.byToken, token)
	return a, true
}

// peek returns the user's album without removing it.
func (s *albumStore) peek(token string, user docsbot.UserID) (convAlbum, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byToken[token]
	if !ok || a.user != user || s.now().Sub(a.created) > s.ttl {
		return convAlbum{}, false
	}
	return a, true
}
