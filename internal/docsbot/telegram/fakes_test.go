package telegram_test

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/docsbot/telegram"
)

// fakeDrive reports a configurable connection state; by default "not connected",
// like the production stub.
type fakeDrive struct {
	connected bool
	revokeErr error // returned by Revoke and Disconnect
	gone      bool  // FileExists reports the remote file as deleted
}

func (d fakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) { return d.connected, nil }

func (fakeDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{}, nil
}

func (fakeDrive) Rename(context.Context, docsbot.UserID, string, string) error { return nil }

func (fakeDrive) Move(context.Context, docsbot.UserID, string, docsbot.Category) error { return nil }

func (fakeDrive) Trash(context.Context, docsbot.UserID, string) error { return nil }

func (d fakeDrive) FileExists(context.Context, docsbot.UserID, string) (bool, error) {
	return !d.gone, nil
}

func (fakeDrive) Replace(context.Context, docsbot.UserID, string, string, []byte) error { return nil }

func (d fakeDrive) Revoke(context.Context, docsbot.UserID) error { return d.revokeErr }

func (d fakeDrive) Disconnect(context.Context, docsbot.UserID) error { return d.revokeErr }

// fakeAuth returns a fixed consent URL for the connect button.
type fakeAuth struct{}

func (fakeAuth) AuthURL(context.Context, string, string) (string, error) {
	return "https://auth.example/consent", nil
}

// fakeRepo is an in-memory docsbot.Repo for handler tests.
type fakeRepo struct {
	folders map[docsbot.Category]string // cached Drive folder ids by category
	docs    map[docsbot.DocumentID]docsbot.Document
	status  *fakeStatus // set by fakeDepsWith
	seq     int
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
		d.CreatedAt = time.Now()
	}
	if d.BackupStatus == "" {
		d.BackupStatus = docsbot.BackupNone // the column default
	}
	r.docs[d.ID] = d
	return d
}

func (r *fakeRepo) CreateDocument(_ context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	return r.seed(docsbot.Document{
		UserID: in.UserID, Status: docsbot.StatusPending, Kind: in.Kind, Title: in.Title,
		TgFileID: in.FileID, TgFileUniqueID: in.FileUniqueID, BackupStatus: docsbot.BackupNone,
		MediaGroupID: in.MediaGroupID, FileSize: in.FileSize, PageCount: in.PageCount, Width: in.Width, Height: in.Height, MIME: in.MIME,
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

func (r *fakeRepo) PendingByMediaGroup(_ context.Context, user docsbot.UserID, group string) ([]docsbot.Document, error) {
	var out []docsbot.Document
	for _, d := range r.docs {
		if d.UserID == user && d.MediaGroupID == group && d.Status == docsbot.StatusPending {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *fakeRepo) IntakeStats(_ context.Context, user docsbot.UserID, since time.Time) (docsbot.IntakeStats, error) {
	var st docsbot.IntakeStats
	for _, d := range r.docs {
		if d.UserID != user {
			continue
		}
		st.Total++
		if d.Status == docsbot.StatusPending {
			st.Pending++
		}
		if d.CreatedAt.After(since) {
			st.Recent++
		}
	}
	return st, nil
}

func (r *fakeRepo) PurgeStalePending(_ context.Context, olderThan time.Time) (int, error) {
	n := 0
	for id, d := range r.docs {
		if d.Status == docsbot.StatusPending && d.CreatedAt.Before(olderThan) {
			delete(r.docs, id)
			n++
		}
	}
	return n, nil
}

func (r *fakeRepo) SaveDocument(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.Status != docsbot.StatusPending {
		return false, nil
	}
	d.Category, d.Status = cat, docsbot.StatusSaved
	r.docs[id] = d
	return true, nil
}

func (*fakeRepo) StuckSyncs(context.Context, time.Time, int) ([]docsbot.StuckUpload, error) {
	return nil, nil
}

func (*fakeRepo) StuckUploads(context.Context, time.Time, int) ([]docsbot.StuckUpload, error) {
	return nil, nil
}

func (r *fakeRepo) ResetPendingUploads(_ context.Context, user docsbot.UserID) error {
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
	return r.setBackup(user, id, docsbot.BackupUploading)
}

func (r *fakeRepo) SetBackupStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	return r.setBackup(user, id, s)
}

// setBackup is a user-scoped update: a foreign or missing document is a no-op.
func (r *fakeRepo) setBackup(user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		d.BackupStatus = s
		r.docs[id] = d
	}
	return nil
}

// ClaimFailed mirrors ClaimFailedForRetry: failed -> pending, once.
func (r *fakeRepo) ClaimFailed(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.BackupStatus != docsbot.BackupFailed {
		return false, nil
	}
	d.BackupStatus = docsbot.BackupPending
	r.docs[id] = d
	return true, nil
}

func (r *fakeRepo) TitleExistsInCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	for _, d := range r.docs {
		if d.UserID == user && d.Category == cat && d.Status == docsbot.StatusSaved && d.Title == title {
			return true, nil
		}
	}
	return false, nil
}

func (r *fakeRepo) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Title = title })
}

func (r *fakeRepo) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Category = cat })
}

// mutate applies fn to the user's document, or ErrNotFound like the :execrows queries.
func (r *fakeRepo) mutate(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	d, ok := r.docs[id]
	if !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	fn(&d)
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
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
	return len(r.savedFor(user)), nil
}

func (r *fakeRepo) CountNotBackedUp(_ context.Context, user docsbot.UserID) (int, error) {
	n := 0
	for _, d := range r.savedFor(user) {
		if !d.InDrive() && d.BackupStatus != docsbot.BackupTooLarge { // mirrors the SQL
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
	for c, n := range byCat { // raw and unordered, like the SQL: the service folds and sorts
		out = append(out, docsbot.CategoryCount{Category: c, Count: n})
	}
	return out, nil
}

func (r *fakeRepo) ListByCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	var all []docsbot.Document
	for _, d := range r.savedFor(user) {
		if d.Category == cat || (cat == docsbot.CatOther && !d.Category.Valid()) {
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

// fakeUploads is a no-op Uploads port: handler tests assert on the repo and the
// sent messages, not on the queue.
// With repo set it also marks the document pending, like the real enqueue.
type fakeUploads struct{ repo *fakeRepo }

func (u fakeUploads) EnqueueUpload(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	if u.repo != nil {
		d := u.repo.docs[id]
		d.BackupStatus = docsbot.BackupPending
		u.repo.docs[id] = d
	}
	return nil
}
func (fakeUploads) RequeueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return nil
}

func (fakeUploads) CancelUploads(context.Context, docsbot.UserID) error { return nil }

func (fakeUploads) CancelUserJobs(context.Context, docsbot.UserID) error { return nil }

// fakeSyncs is a docsbot.Syncs over fakeRepo that mirrors the SQL decisions: a
// backed-up document with a live file goes sync-pending; no job is run.
type fakeSyncs struct{ repo *fakeRepo }

func (s fakeSyncs) change(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	fn(&d)
	d.MetaVersion++
	queued := d.BackupStatus == docsbot.BackupBackedUp && d.SyncStatus != docsbot.SyncMissing
	if queued {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return queued, nil
}

func (s fakeSyncs) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) (bool, error) {
	return s.change(user, id, func(d *docsbot.Document) { d.Title = title })
}

func (s fakeSyncs) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (bool, error) {
	return s.change(user, id, func(d *docsbot.Document) { d.Category = cat })
}

func (fakeSyncs) EnqueueSync(context.Context, docsbot.UserID, docsbot.DocumentID) error { return nil }

func (s fakeSyncs) MarkBackedUp(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	d.BackupStatus, d.DriveFileID = docsbot.BackupBackedUp, driveFileID
	if pages > 0 {
		d.PageCount = pages
	}
	queued := d.MetaVersion != startVersion
	if queued {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return queued, nil
}

// ReplaceContent mirrors ReplaceDocumentContent: the guarded update and the
// unique (user, file) constraint.
func (s fakeSyncs) ReplaceContent(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user ||
		d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading ||
		d.SyncStatus == docsbot.SyncPending || d.SyncStatus == docsbot.SyncMissing {
		return false, nil
	}
	for _, o := range s.repo.docs {
		if o.ID != id && o.UserID == user && o.TgFileUniqueID == in.FileUniqueID {
			return false, docsbot.ErrDuplicateFile
		}
	}
	d.Kind, d.TgFileID, d.TgFileUniqueID, d.FileSize, d.MIME, d.PageCount = in.Kind, in.FileID, in.FileUniqueID, in.FileSize, in.MIME, in.PageCount
	d.Width, d.Height = in.Width, in.Height
	d.MetaVersion++
	if cloud && d.BackupStatus == docsbot.BackupBackedUp {
		d.ContentDirty = true
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.repo.docs[id] = d
	return true, nil
}

func (s fakeSyncs) RetrySync(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user || d.SyncStatus != docsbot.SyncFailed {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	s.repo.docs[id] = d
	return true, nil
}

func (s fakeSyncs) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, trash bool) (bool, error) {
	d, ok := s.repo.docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	delete(s.repo.docs, id)
	live := d.BackupStatus == docsbot.BackupBackedUp && d.DriveFileID != "" && d.SyncStatus != docsbot.SyncMissing
	return live && !trash, nil
}

func (fakeSyncs) RequeueSync(context.Context, docsbot.UserID, docsbot.DocumentID) error { return nil }

// fakeDeps builds a Service over in-memory fakes, plus the fake authorizer.
func fakeDeps() (*docsbot.Service, telegram.Authorizer, *fakeRepo) {
	return fakeDepsWith(fakeDrive{})
}

// fakeDepsWith is fakeDeps with a chosen Drive state. Files are never reached
// and uploads is a no-op.
func fakeDepsWith(drive fakeDrive) (*docsbot.Service, telegram.Authorizer, *fakeRepo) {
	repo := newFakeRepo()
	repo.status = newFakeStatus(repo)
	return docsbot.NewService(repo, drive, nil, fakeUploads{repo: repo}, repo, repo.status, fakeSyncs{repo: repo}), fakeAuth{}, repo
}

func (r *fakeRepo) MarkSynced(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, version int) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.MetaVersion != version || d.SyncStatus != docsbot.SyncPending {
		return false, nil
	}
	d.SyncStatus, d.SyncReason, d.ContentDirty = docsbot.SyncOK, "", false
	r.docs[id] = d
	return true, nil
}

// TransitionSyncStatus mirrors the conditional UPDATE.
func (r *fakeRepo) TransitionSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, from, to docsbot.SyncStatus) (bool, error) {
	d, ok := r.docs[id]
	if !ok || d.UserID != user || d.SyncStatus != from || d.BackupStatus != docsbot.BackupBackedUp {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = to, ""
	r.docs[id] = d
	return true, nil
}

func (r *fakeRepo) SetSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.SyncStatus, reason string) error {
	if d, ok := r.docs[id]; ok && d.UserID == user {
		d.SyncStatus, d.SyncReason = s, reason
		r.docs[id] = d
	}
	return nil
}

// ClaimTransferable mirrors ClaimTransferableDocuments: none/failed -> pending.
func (r *fakeRepo) ClaimTransferable(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupNone, docsbot.BackupFailed), nil
}

// ClaimAllFailed mirrors ClaimFailedDocuments: failed -> pending.
func (r *fakeRepo) ClaimAllFailed(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupFailed), nil
}

func (r *fakeRepo) claim(user docsbot.UserID, from ...docsbot.BackupStatus) []docsbot.DocumentID {
	var out []docsbot.DocumentID
	for _, d := range r.savedFor(user) {
		if slices.Contains(from, d.BackupStatus) {
			d.BackupStatus = docsbot.BackupPending
			r.docs[d.ID] = d
			out = append(out, d.ID)
		}
	}
	return out
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

func (r *fakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	docs := r.savedFor(user)
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) }) // oldest first, like the SQL
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

func (r *fakeRepo) DeleteAllFolders(_ context.Context, _ docsbot.UserID) error {
	clear(r.folders)
	return nil
}

func (r *fakeRepo) DeleteUserData(_ context.Context, user docsbot.UserID) error {
	for id, d := range r.docs {
		if d.UserID == user {
			delete(r.docs, id)
		}
	}
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

func (s *fakeStatus) Create(_ context.Context, user docsbot.UserID, chatID int64, messageID int, kind docsbot.StatusKind) (docsbot.StatusMessage, error) {
	s.seq++
	m := docsbot.StatusMessage{
		ID:        docsbot.StatusMessageID(fmt.Sprintf("sm-%d", s.seq)),
		UserID:    user,
		ChatID:    chatID,
		MessageID: messageID,
		Kind:      kind,
		CreatedAt: time.Now(),
	}
	s.msgs[m.ID] = m
	return m, nil
}

func (s *fakeStatus) Close(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) error {
	if m, ok := s.msgs[id]; ok && m.UserID == user {
		m.Closed = true
		s.msgs[id] = m
	}
	return nil
}

func (s *fakeStatus) SetMessageID(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID, messageID int) error {
	if m, ok := s.msgs[id]; ok && m.UserID == user {
		m.MessageID = messageID
		s.msgs[id] = m
	}
	return nil
}

func (s *fakeStatus) Attach(_ context.Context, user docsbot.UserID, doc docsbot.DocumentID, id docsbot.StatusMessageID) error {
	if d, ok := s.repo.docs[doc]; ok && d.UserID == user {
		d.StatusMessageID = id
		s.repo.docs[doc] = d
	}
	return nil
}

func (s *fakeStatus) DocumentsIn(_ context.Context, user docsbot.UserID, id docsbot.StatusMessageID) ([]docsbot.Document, error) {
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
	m, ok := s.msgs[id]
	if !ok || m.UserID != user {
		return docsbot.StatusMessage{}, docsbot.ErrNotFound
	}
	return m, nil
}
