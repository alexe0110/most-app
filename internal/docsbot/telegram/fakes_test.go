package telegram_test

import (
	"context"
	"fmt"
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
}

func (d fakeDrive) Connected(context.Context, docsbot.UserID) (bool, error) { return d.connected, nil }

func (fakeDrive) Upload(context.Context, docsbot.UserID, docsbot.DriveUpload) (docsbot.DriveFile, error) {
	return docsbot.DriveFile{}, nil
}

func (fakeDrive) Revoke(context.Context, docsbot.UserID) error { return nil }

// fakeAuth returns a fixed consent URL for the connect button.
type fakeAuth struct{}

func (fakeAuth) AuthURL(context.Context, string, string) (string, error) {
	return "https://auth.example/consent", nil
}

// fakeRepo is an in-memory docsbot.Repo for handler tests.
type fakeRepo struct {
	folders map[docsbot.Category]string // cached Drive folder ids by category
	docs    map[docsbot.DocumentID]docsbot.Document
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

// fakeUploads is a no-op Uploads port: handler tests assert on the repo and the
// sent messages, not on the queue.
type fakeUploads struct{}

func (fakeUploads) EnqueueUpload(context.Context, docsbot.UserID, docsbot.DocumentID) error {
	return nil
}
func (fakeUploads) CancelUploads(context.Context, docsbot.UserID) error { return nil }

// fakeDeps builds a Service over in-memory fakes, plus the fake authorizer.
func fakeDeps() (*docsbot.Service, telegram.Authorizer, *fakeRepo) {
	repo := newFakeRepo()
	// Drive is never connected here, so files are not reached; uploads is a no-op.
	return docsbot.NewService(repo, fakeDrive{}, nil, fakeUploads{}, repo), fakeAuth{}, repo
}

func (r *fakeRepo) MarkBackedUp(_ context.Context, id docsbot.DocumentID, driveFileID string) error {
	d := r.docs[id]
	d.BackupStatus, d.DriveFileID = docsbot.BackupBackedUp, driveFileID
	r.docs[id] = d
	return nil
}

func (r *fakeRepo) NotBackedUpIDs(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	var out []docsbot.DocumentID
	for _, d := range r.savedFor(user) {
		if !d.InDrive() {
			out = append(out, d.ID)
		}
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
	var out []docsbot.DocumentID
	for _, d := range r.savedFor(user) {
		if d.BackupStatus == docsbot.BackupFailed {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

func (r *fakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	docs := r.savedFor(user)
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
	return nil
}

var _ docsbot.Folders = (*fakeRepo)(nil)

func (r *fakeRepo) CategoryFolderID(_ context.Context, _ docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	id, ok := r.folders[cat]
	return id, ok, nil
}
