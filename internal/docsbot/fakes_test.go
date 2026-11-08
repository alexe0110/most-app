package docsbot_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	repo    *fakeRepo
	calls   []docsbot.DocumentID
	cancels []docsbot.UserID
}

func (u *fakeUploads) EnqueueUpload(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	u.calls = append(u.calls, id)
	d := u.repo.docs[id]
	d.BackupStatus = docsbot.BackupPending
	u.repo.docs[id] = d
	return nil
}

func (u *fakeUploads) CancelUploads(_ context.Context, user docsbot.UserID) error {
	u.cancels = append(u.cancels, user)
	return nil
}

// fakeRepo is an in-memory docsbot.Repo for unit tests.
type fakeRepo struct {
	folders map[docsbot.Category]string // cached Drive folder ids by category
	docs    map[docsbot.DocumentID]docsbot.Document
	seq     int
	deleted []docsbot.UserID // users passed to DeleteUserData
}

var _ docsbot.Repo = (*fakeRepo)(nil)

func newFakeRepo() *fakeRepo {
	return &fakeRepo{docs: map[docsbot.DocumentID]docsbot.Document{}}
}

// seed stores a document, assigning an id and creation time if absent.
func (r *fakeRepo) seed(d docsbot.Document) docsbot.Document {
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
	return r.seed(docsbot.Document{
		UserID: in.UserID, Status: docsbot.StatusPending, Kind: in.Kind, Title: in.Title,
		TgFileID: in.FileID, TgFileUniqueID: in.FileUniqueID, BackupStatus: docsbot.BackupNone,
	}), nil
}

func (r *fakeRepo) DocumentByFileUniqueID(_ context.Context, user docsbot.UserID, uid string) (docsbot.Document, error) {
	for _, d := range r.docs {
		if d.UserID == user && d.TgFileUniqueID == uid {
			return d, nil
		}
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) DocumentByID(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		return d, nil
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *fakeRepo) SaveDocument(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	d := r.docs[id]
	d.Category, d.Status = cat, docsbot.StatusSaved
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) SetBackupStatus(_ context.Context, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	d := r.docs[id]
	d.BackupStatus = s
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) SetTitle(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID, title string) error {
	d := r.docs[id]
	d.Title = title
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) SetCategory(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	d := r.docs[id]
	d.Category = cat
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) DeleteDocument(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
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
	return len(r.savedFor(user)), nil
}

func (r *fakeRepo) CountNotBackedUp(_ context.Context, user docsbot.UserID) (int, error) {
	n := 0
	for _, d := range r.savedFor(user) {
		if !d.InDrive() {
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) CategoryCounts(_ context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
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

func (r *fakeRepo) MarkBackedUp(_ context.Context, id docsbot.DocumentID, driveFileID string) error {
	d := r.docs[id]
	d.BackupStatus, d.DriveFileID = docsbot.BackupBackedUp, driveFileID
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) NotBackedUpIDs(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	var docs []docsbot.Document
	for _, d := range r.savedFor(user) {
		if !d.InDrive() {
			docs = append(docs, d)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	out := make([]docsbot.DocumentID, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	return out, nil
}

func (r *fakeRepo) BackupStatusCounts(_ context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
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

func (r *fakeRepo) FailedIDs(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	var docs []docsbot.Document
	for _, d := range r.savedFor(user) {
		if d.BackupStatus == docsbot.BackupFailed {
			docs = append(docs, d)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	out := make([]docsbot.DocumentID, 0, len(docs))
	for _, d := range docs {
		out = append(out, d.ID)
	}
	return out, nil
}

func (r *fakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	docs := r.savedFor(user)
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

func (r *fakeRepo) DeleteUserData(_ context.Context, user docsbot.UserID) error {
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
	id, ok := r.folders[cat]
	return id, ok, nil
}

// fakeStatus is an in-memory docsbot.StatusMessages over the fake repo's documents.
type fakeStatus struct {
	repo *fakeRepo
	msgs map[docsbot.StatusMessageID]docsbot.StatusMessage
	seq  int
}

var _ docsbot.StatusMessages = (*fakeStatus)(nil)

func newFakeStatus(repo *fakeRepo) *fakeStatus {
	return &fakeStatus{repo: repo, msgs: map[docsbot.StatusMessageID]docsbot.StatusMessage{}}
}

func (s *fakeStatus) OpenForUser(_ context.Context, user docsbot.UserID) (docsbot.StatusMessage, bool, error) {
	var best docsbot.StatusMessage
	found := false
	for _, m := range s.msgs {
		if m.UserID == user && !m.Closed && (!found || m.CreatedAt.After(best.CreatedAt)) {
			best, found = m, true
		}
	}
	return best, found, nil
}

func (s *fakeStatus) Create(_ context.Context, user docsbot.UserID, chatID int64, messageID int) (docsbot.StatusMessage, error) {
	s.seq++
	m := docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(fmt.Sprintf("sm-%d", s.seq)),
		UserID:    user,
		ChatID:    chatID,
		MessageID: messageID,
	}
	s.msgs[m.ID] = m
	return m, nil
}

func (s *fakeStatus) Close(_ context.Context, id docsbot.StatusMessageID) error {
	m := s.msgs[id]
	m.Closed = true
	s.msgs[id] = m
	return nil
}

func (s *fakeStatus) SetMessageID(_ context.Context, id docsbot.StatusMessageID, messageID int) error {
	m := s.msgs[id]
	m.MessageID = messageID
	s.msgs[id] = m
	return nil
}

func (s *fakeStatus) Attach(_ context.Context, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	d := s.repo.docs[doc]
	d.StatusMessageID = id
	s.repo.docs[doc] = d
	return nil
}

func (s *fakeStatus) DocumentsIn(_ context.Context, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
	var out []docsbot.Document
	for _, d := range s.repo.docs {
		if d.StatusMessageID == id {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStatus) ByID(_ context.Context, id docsbot.StatusMessageID) (docsbot.StatusMessage, error) {
	m, ok := s.msgs[id]
	if !ok {
		return docsbot.StatusMessage{}, docsbot.ErrNotFound
	}
	return m, nil
}
