package docsbot_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// fakeDrive reports a configurable connection state and upload outcome.
type fakeDrive struct {
	connected  bool
	err        error
	uploadFile docsbot.DriveFile
	uploadErr  error
	uploads    *int  // counts Upload calls when set
	revokeErr  error // returned by Revoke
	revoked    *bool // set to true on Revoke when provided

	disconnectErr error // returned by Disconnect
	disconnected  *bool // set to true on Disconnect when provided
}

func (d fakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) {
	return d.connected, d.err
}

func (d fakeDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	if d.uploads != nil {
		*d.uploads++
	}
	return d.uploadFile, d.uploadErr
}

func (d fakeDrive) Revoke(context.Context, docsbot.UserID) error {
	if d.revoked != nil {
		*d.revoked = true
	}
	return d.revokeErr
}

func (d fakeDrive) Disconnect(context.Context, docsbot.UserID) error {
	if d.disconnected != nil {
		*d.disconnected = true
	}
	return d.disconnectErr
}

// fakeFiles returns a configurable download result.
type fakeFiles struct {
	data      []byte
	mime      string
	err       error
	downloads *int // counts Download calls when set
}

func (f fakeFiles) Download(context.Context, string) ([]byte, string, error) {
	if f.downloads != nil {
		*f.downloads++
	}
	return f.data, f.mime, f.err
}

// fakeUploads records enqueued uploads and, like the real atomic enqueue, marks
// the document pending.
type fakeUploads struct {
	mu       sync.Mutex
	repo     *fakeRepo
	calls    []docsbot.DocumentID
	cancels  []docsbot.UserID
	requeued []docsbot.DocumentID
}

func (u *fakeUploads) EnqueueUpload(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, id)
	u.repo.mu.Lock()
	defer u.repo.mu.Unlock()
	if d, ok := u.repo.docs[id]; ok && d.UserID == user {
		d.BackupStatus = docsbot.BackupPending
		u.repo.docs[id] = d
	}
	return nil
}

// RequeueUpload records the call; like the real one it leaves backup_status alone.
func (u *fakeUploads) RequeueUpload(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requeued = append(u.requeued, id)
	return nil
}

func (u *fakeUploads) CancelUploads(_ context.Context, user docsbot.UserID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cancels = append(u.cancels, user)
	return nil
}

// fakeRepo is an in-memory docsbot.Repo for unit tests.
type fakeRepo struct {
	mu      sync.Mutex                  // guards every field below; fakes are used by parallel tests
	folders map[docsbot.Category]string // cached Drive folder ids by category
	docs    map[docsbot.DocumentID]docsbot.Document
	seq     int
	deleted []docsbot.UserID                 // users passed to DeleteUserData
	updated map[docsbot.DocumentID]time.Time // last update time, set by tests for the sweeper
}

var _ docsbot.Repo = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{docs: map[docsbot.DocumentID]docsbot.Document{}}
}

// seed stores a document, assigning an id and creation time if absent.
func (r *fakeRepo) seed(d docsbot.Document) docsbot.Document {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seedLocked(d)
}

// seedLocked is seed for callers already holding r.mu.
func (r *fakeRepo) seedLocked(d docsbot.Document) docsbot.Document {
	if d.ID == "" {
		r.seq++
		d.ID = docsbot.DocumentID(fmt.Sprintf("doc-%d", r.seq))
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Unix(int64(r.seq), 0)
	}
	if d.BackupStatus == "" {
		d.BackupStatus = docsbot.BackupNone
	}
	r.docs[d.ID] = d
	return d
}

func (r *fakeRepo) CreateDocument(_ context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seedLocked(docsbot.Document{
		UserID: in.UserID, Status: docsbot.StatusPending, Kind: in.Kind, Title: in.Title,
		TgFileID: in.FileID, TgFileUniqueID: in.FileUniqueID, BackupStatus: docsbot.BackupNone,
	}), nil
}

func (r *fakeRepo) DocumentByFileUniqueID(_ context.Context, user docsbot.UserID, uid string) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.UserID == user && d.TgFileUniqueID == uid {
			return d, nil
		}
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) DocumentByID(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.docs[id]; ok && d.UserID == user {
		return d, nil
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) SaveDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docs[id]
	if !ok || d.UserID != user {
		return nil
	}
	d.Category, d.Status = cat, docsbot.StatusSaved
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) ResetPendingUploads(_ context.Context, user docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, d := range r.docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved &&
			(d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading) {
			d.BackupStatus = docsbot.BackupNone
			r.docs[id] = d
		}
	}
	return nil
}

func (r *fakeRepo) MarkUploading(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return r.update(user, id, func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupUploading })
}

func (r *fakeRepo) SetBackupStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	return r.update(user, id, func(d *docsbot.Document) { d.BackupStatus = s })
}

// update applies fn to the user's document under the lock. Like the real
// user-scoped UPDATE, a missing or foreign document is a silent no-op.
func (r *fakeRepo) update(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.docs[id]; ok && d.UserID == user {
		fn(&d)
		r.docs[id] = d
	}
	return nil
}

// ClaimFailed mirrors ClaimFailedForRetry: failed -> pending, once.
func (r *fakeRepo) ClaimFailed(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.BackupStatus != docsbot.BackupFailed {
		return false, nil
	}
	d.BackupStatus = docsbot.BackupPending
	r.docs[id] = d
	return true, nil
}

// StuckUploads mirrors the real query, assuming every user has an active
// integration (the join is covered by the integration tests).
func (r *fakeRepo) StuckUploads(_ context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docsbot.StuckUpload
	for id, d := range r.docs {
		bs := d.BackupStatus
		if d.Status != docsbot.StatusSaved || (bs != docsbot.BackupPending && bs != docsbot.BackupUploading) {
			continue
		}
		if at, ok := r.updated[id]; ok && at.Before(olderThan) {
			out = append(out, docsbot.StuckUpload{User: d.UserID, ID: id, UpdatedAt: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeRepo) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Title = title })
}

// mutate is update that reports ErrNotFound for a missing or foreign document,
// like the :execrows queries.
func (r *fakeRepo) mutate(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docs[id]
	if !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	fn(&d)
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) TitleExistsInCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.docs {
		if d.UserID == user && d.Category == cat && d.Status == docsbot.StatusSaved && d.Title == title {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeRepo) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Category = cat })
}

func (r *fakeRepo) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.docs[id]; !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	delete(r.docs, id)
	return nil
}

func (r *fakeRepo) savedFor(user docsbot.UserID) []docsbot.Document {
	var out []docsbot.Document
	for _, d := range r.docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved {
			out = append(out, d)
		}
	}
	return out
}

func (r *fakeRepo) CountSaved(_ context.Context, user docsbot.UserID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.savedFor(user)), nil
}

func (r *fakeRepo) CountNotBackedUp(_ context.Context, user docsbot.UserID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.savedFor(user) {
		if !d.InDrive() {
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) CategoryCounts(_ context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byCat := map[docsbot.Category]int{}
	for _, d := range r.savedFor(user) {
		byCat[d.Category]++
	}
	var out []docsbot.CategoryCount
	for _, c := range docsbot.AllCategories {
		if n, ok := byCat[c]; ok {
			out = append(out, docsbot.CategoryCount{Category: c, Count: n})
		}
	}
	return out, nil
}

func (r *fakeRepo) ListByCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []docsbot.Document
	for _, d := range r.savedFor(user) {
		if d.Category == cat {
			all = append(all, d)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset >= len(all) {
		return nil, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], nil
}

func (r *fakeRepo) Search(_ context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q = strings.ToLower(q)
	var out []docsbot.Document
	for _, d := range r.savedFor(user) {
		if strings.Contains(strings.ToLower(d.Title), q) {
			out = append(out, d)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *fakeRepo) MarkBackedUp(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string) error {
	return r.update(user, id, func(d *docsbot.Document) {
		d.BackupStatus, d.DriveFileID = docsbot.BackupBackedUp, driveFileID
	})
}

// ClaimTransferable mirrors ClaimTransferableDocuments: none/failed -> pending, atomically.
func (r *fakeRepo) ClaimTransferable(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupNone, docsbot.BackupFailed), nil
}

// ClaimAllFailed mirrors ClaimFailedDocuments: failed -> pending, atomically.
func (r *fakeRepo) ClaimAllFailed(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupFailed), nil
}

// claim atomically (under the lock) moves the user's saved documents in one of
// the given states to pending and returns their ids, oldest first.
func (r *fakeRepo) claim(user docsbot.UserID, from ...docsbot.BackupStatus) []docsbot.DocumentID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var docs []docsbot.Document
	for _, d := range r.savedFor(user) {
		if slices.Contains(from, d.BackupStatus) {
			docs = append(docs, d)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	out := make([]docsbot.DocumentID, 0, len(docs))
	for _, d := range docs {
		d.BackupStatus = docsbot.BackupPending
		r.docs[d.ID] = d
		out = append(out, d.ID)
	}
	return out
}

func (r *fakeRepo) BackupStatusCounts(_ context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byStatus := map[docsbot.BackupStatus]int{}
	for _, d := range r.savedFor(user) {
		byStatus[d.BackupStatus]++
	}
	out := make([]docsbot.BackupStatusCount, 0, len(byStatus))
	for st, n := range byStatus {
		out = append(out, docsbot.BackupStatusCount{Status: st, Count: n})
	}
	return out, nil
}

func (r *fakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	docs := r.savedFor(user)
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

func (r *fakeRepo) DeleteUserData(_ context.Context, user docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, d := range r.docs {
		if d.UserID == user {
			delete(r.docs, id)
		}
	}
	r.deleted = append(r.deleted, user)
	return nil
}

var _ docsbot.Folders = (*fakeRepo)(nil)

func (r *fakeRepo) CategoryFolderID(_ context.Context, _ docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.folders[cat]
	return id, ok, nil
}

func (r *fakeRepo) DeleteAllFolders(_ context.Context, _ docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.folders)
	return nil
}

// fakeStatus is an in-memory docsbot.StatusMessages over the fake repo's documents.
type fakeStatus struct {
	mu   sync.Mutex // guards msgs and seq; documents are guarded by repo.mu
	repo *fakeRepo
	msgs map[docsbot.StatusMessageID]docsbot.StatusMessage
	seq  int
}

var _ docsbot.StatusMessages = (*fakeStatus)(nil)

func newFakeStatus(repo *fakeRepo) *fakeStatus {
	return &fakeStatus{repo: repo, msgs: map[docsbot.StatusMessageID]docsbot.StatusMessage{}}
}

func (s *fakeStatus) OpenForUser(_ context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best docsbot.StatusMessage
	found := false
	for _, m := range s.msgs {
		if m.UserID == user && !m.Closed && (!found || m.CreatedAt.After(best.CreatedAt)) {
			best, found = m, true
		}
	}
	return best, found, nil
}

func (s *fakeStatus) Create(_ context.Context, user docsbot.UserID, chatID int64, messageID int, kind docsbot.StatusKind) (docsbot.StatusMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	m := docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(fmt.Sprintf("sm-%d", s.seq)),
		UserID:    user,
		ChatID:    chatID,
		MessageID: messageID,
		Kind:      kind,
	}
	s.msgs[m.ID] = m
	return m, nil
}

func (s *fakeStatus) Close(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error {
	return s.update(user, id, func(m *docsbot.StatusMessage) { m.Closed = true })
}

// update applies fn to the user's status message; a foreign one is a silent no-op.
func (s *fakeStatus) update(user docsbot.UserID, id docsbot.StatusMessageID, fn func(*docsbot.StatusMessage)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.msgs[id]; ok && m.UserID == user {
		fn(&m)
		s.msgs[id] = m
	}
	return nil
}

func (s *fakeStatus) SetMessageID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error {
	return s.update(user, id, func(m *docsbot.StatusMessage) { m.MessageID = messageID })
}

func (s *fakeStatus) Attach(_ context.Context, user docsbot.UserID, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	if d, ok := s.repo.docs[doc]; ok && d.UserID == user {
		d.StatusMessageID = id
		s.repo.docs[doc] = d
	}
	return nil
}

func (s *fakeStatus) DocumentsIn(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	var out []docsbot.Document
	for _, d := range s.repo.docs {
		if d.UserID == user && d.StatusMessageID == id {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStatus) ByID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.msgs[id]
	if !ok || m.UserID != user {
		return docsbot.StatusMessage{}, docsbot.ErrNotFound
	}
	return m, nil
}
