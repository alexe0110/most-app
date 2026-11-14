package telegram

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
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
	stepAwaitPDF                 // PDF actions: waiting for the PDF
	stepPDFMenu                  // PDF actions: choosing the operation
)

// pendingImage is an image collected for a PDF. Only the Telegram file id and the
// size are kept: the bytes are downloaded when the PDF is built, so a session
// full of photos holds no image data in memory. msgID keeps album order (album
// photos arrive as separate updates). The file id is never logged.
type pendingImage struct {
	msgID  int
	fileID string
	size   int64 // as Telegram declared it; 0 when unknown
}

// session is one user's in-progress conversion, held only in memory.
type session struct {
	step     step
	msgID    int            // the dialogue message whose buttons drive the session; 0 until known
	images   []pendingImage // PDF from photos: the pages collected so far
	imageSum int64          // PDF from photos: declared size of images
	source   []byte         // compress: the one image, already checked to be a photo (at most tg.MaxFileBytes)
	maxBytes int
	inputPNG bool // compress: the image is a PNG, so the format is worth asking
	updated  time.Time

	// PDF operations on a stored document (card or picker).
	sourceDoc   docsbot.DocumentID // "" when the PDF was sent to /convert
	sourceTitle string             // stored title, or the sent file's name
	pdfOp       string             // opKeep, opDrop or opMerge
	pdfs        []pdfPart          // keep/drop: the one source; merge: source first, then the added
	pdfBytes    int                // total size of pdfs
}

// pdfPart is a PDF a session will work on. Only the Telegram file id and the
// size are kept: the bytes are downloaded when the operation runs, so an idle
// session holds no documents in memory. The file id is never logged.
type pdfPart struct {
	fileID string
	size   int
}

// orderedImages returns the collected images in arrival order.
func (s *session) orderedImages() []pendingImage {
	sorted := slices.Clone(s.images)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].msgID < sorted[j].msgID })
	return sorted
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

// liveLocked is live for a caller that holds the lock. Every access also drops
// all timed-out sessions, so abandoned ones (and the image ids they hold) don't
// pile up until their owners come back.
func (s *sessionStore) liveLocked(user int64) (*session, bool) {
	s.sweepLocked()
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

// sweepLocked deletes every session idle for longer than the TTL.
func (s *sessionStore) sweepLocked() {
	now := s.now()
	for user, sess := range s.byUser {
		if now.Sub(sess.updated) > s.ttl {
			delete(s.byUser, user)
		}
	}
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
	s.sweepLocked()
	s.byUser[user] = &session{step: stepChooseAction, updated: s.now()}
}

// msgMatches reports whether a press on message msgID belongs to the session's
// dialogue message. A press id of 0 means unknown (a typed message drives the
// step) and is accepted: real Telegram message ids are never 0. A session whose
// dialogue message is unknown (the send failed) has no current button, so a press
// on any real message is stale.
func (s *session) msgMatches(msgID int) bool {
	return msgID == 0 || (s.msgID != 0 && s.msgID == msgID)
}

// atStep reports whether the session is at one of the steps; no steps means any.
func (s *session) atStep(steps []step) bool {
	return len(steps) == 0 || slices.Contains(steps, s.step)
}

// check returns a copy of the user's live session if a press on message msgID
// is valid for one of the steps (none means any step).
func (s *sessionStore) check(user int64, msgID int, steps ...step) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.liveLocked(user)
	if !ok || !sess.msgMatches(msgID) || !sess.atStep(steps) {
		return session{}, false
	}
	return *sess, true
}

// advance validates a button press like check and, if valid, applies fn under
// the same lock, so a double tap moves the session once. It returns a copy of
// the updated session.
func (s *sessionStore) advance(user int64, msgID int, from []step, fn func(*session)) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.liveLocked(user)
	if !ok || !sess.msgMatches(msgID) || !sess.atStep(from) {
		return session{}, false
	}
	fn(sess)
	sess.updated = s.now()
	return *sess, true
}

// setMsg records the session's dialogue message.
func (s *sessionStore) setMsg(user int64, msgID int) {
	s.update(user, func(sess *session) { sess.msgID = msgID })
}

// claim removes the user's session if it is live, belongs to message msgID (0
// skips the check) and is at one of the steps (none means any), and returns it.
// The caller owns it: a second concurrent claim finds nothing, so an operation
// can't run twice. restore puts it back when the user can retry.
func (s *sessionStore) claim(user int64, msgID int, at ...step) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.liveLocked(user)
	if !ok || !sess.msgMatches(msgID) || !sess.atStep(at) {
		return session{}, false
	}
	delete(s.byUser, user)
	return *sess, true
}

// restore puts a claimed session back, unless the user started another one.
func (s *sessionStore) restore(user int64, sess session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
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

// tokenMap holds values by a short token that rides in a button's callback data
// (a file id would not fit, and must not be logged). Each value belongs to one
// owner, a Telegram user id, and is taken at most once. Memory only: a restart or
// the TTL drops it, and the chat message stays.
type tokenMap[V any] struct {
	m *ttlMap[string, owned[V]]
}

type owned[V any] struct {
	owner int64
	v     V
}

func newTokenMap[V any](now func() time.Time) *tokenMap[V] {
	return &tokenMap[V]{m: newTTLMap[string, owned[V]](sessionTTL, maxStoreEntries, now)}
}

// newToken returns a random 16-character token.
func newToken() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails on supported platforms
	return hex.EncodeToString(raw[:])
}

// put stores v for owner under token, renewing its TTL.
func (s *tokenMap[V]) put(token string, owner int64, v V) {
	s.m.set(token, owned[V]{owner: owner, v: v})
}

// take removes and returns the owner's value, so a double tap acts once. A token
// of another owner, an unknown or an expired one reports false; a foreign
// attempt does not burn the owner's token.
func (s *tokenMap[V]) take(token string, owner int64) (V, bool) {
	e, ok := s.m.takeIf(token, func(e owned[V]) bool { return e.owner == owner })
	return e.v, ok
}

// peek returns the owner's value without removing it.
func (s *tokenMap[V]) peek(token string, owner int64) (V, bool) {
	e, ok := s.m.get(token)
	if !ok || e.owner != owner {
		var zero V
		return zero, false
	}
	return e.v, true
}

// keyedMutex is a mutex per key (a user id) that forgets idle keys.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[int64]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

func newKeyedMutex() keyedMutex { return keyedMutex{entries: map[int64]*keyedEntry{}} }

// lock takes the key's mutex and returns the function that releases it.
func (k *keyedMutex) lock(key int64) (unlock func()) {
	k.mu.Lock()
	e, ok := k.entries[key]
	if !ok {
		e = &keyedEntry{}
		k.entries[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		defer k.mu.Unlock()
		if e.refs--; e.refs == 0 {
			delete(k.entries, key)
		}
	}
}

// busy reports whether another caller waits for the key's mutex while the
// current holder works.
func (k *keyedMutex) busy(key int64) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.entries[key]
	return ok && e.refs > 1
}
