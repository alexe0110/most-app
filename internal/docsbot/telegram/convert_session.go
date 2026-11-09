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
	stepAwaitRange               // PDF keep/drop: waiting for the page range
	stepMergePDF                 // PDF merge: collecting PDFs
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

	// PDF operations on a stored document (card or picker).
	sourceDoc   docsbot.DocumentID
	sourceTitle string
	pdfOp       string    // opKeep, opDrop or opMerge
	pdfs        []pdfPart // keep/drop: the one source; merge: source first, then the added
	pdfBytes    int       // total size of pdfs
}

// pdfPart is a PDF a session will work on. Only the Telegram file id and the
// size are kept: the bytes are downloaded when the operation runs, so an idle
// session holds no documents in memory. The file id is never logged.
type pdfPart struct {
	fileID string
	size   int
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

// claim removes the user's session if it is live and at the given step, and
// returns it. The caller owns it: a second concurrent claim finds nothing, so an
// operation can't run twice. restore puts it back when the user can retry.
func (s *sessionStore) claim(user int64, at step) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.liveLocked(user)
	if !ok || sess.step != at {
		return session{}, false
	}
	delete(s.byUser, user)
	return *sess, true
}

// restore puts a claimed session back, unless the user started another one.
func (s *sessionStore) restore(user int64, sess session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.byUser[user]; taken {
		return
	}
	sess.updated = s.now()
	s.byUser[user] = &sess
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
}

// convAlbum is an album of images offered as one PDF.
type convAlbum struct {
	user   docsbot.UserID
	group  string
	images []albumImage // ordered by message id
}

// tokenStore holds values by a short token that rides in a button's callback
// data (a file id would not fit, and must not be logged). Each value belongs to
// one owner and is taken at most once. Memory only: a restart or the TTL drops
// it, and the chat message stays.
type tokenStore[T any, O comparable] struct {
	mu      sync.Mutex
	byToken map[string]tokenEntry[T]
	ttl     time.Duration
	now     func() time.Time
	ownerOf func(T) O
}

type tokenEntry[T any] struct {
	v       T
	created time.Time
}

type (
	resultStore = tokenStore[convResult, int64]
	albumStore  = tokenStore[convAlbum, docsbot.UserID]
)

func newTokenStore[T any, O comparable](now func() time.Time, ownerOf func(T) O) *tokenStore[T, O] {
	return &tokenStore[T, O]{byToken: map[string]tokenEntry[T]{}, ttl: sessionTTL, now: now, ownerOf: ownerOf}
}

func newResultStore(now func() time.Time) *resultStore {
	return newTokenStore(now, func(r convResult) int64 { return r.tgUser })
}

func newAlbumStore(now func() time.Time) *albumStore {
	return newTokenStore(now, func(a convAlbum) docsbot.UserID { return a.user })
}

// newToken returns a random 16-character token.
func newToken() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails on supported platforms
	return hex.EncodeToString(raw[:])
}

// put stores v under token (renewing its TTL) and drops expired entries.
func (s *tokenStore[T, O]) put(token string, v T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.byToken {
		if now.Sub(e.created) > s.ttl {
			delete(s.byToken, k)
		}
	}
	s.byToken[token] = tokenEntry[T]{v: v, created: now}
}

// take removes and returns the owner's value, so a double tap acts once. A token
// of another owner, an unknown or an expired one reports false; a foreign
// attempt does not burn the owner's token.
func (s *tokenStore[T, O]) take(token string, owner O) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.peekLocked(token, owner)
	if ok {
		delete(s.byToken, token)
	}
	return v, ok
}

// peek returns the owner's value without removing it.
func (s *tokenStore[T, O]) peek(token string, owner O) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peekLocked(token, owner)
}

func (s *tokenStore[T, O]) peekLocked(token string, owner O) (T, bool) {
	var zero T
	e, ok := s.byToken[token]
	if !ok || s.ownerOf(e.v) != owner || s.now().Sub(e.created) > s.ttl {
		return zero, false
	}
	return e.v, true
}
