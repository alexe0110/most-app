package docsbottest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/alexe0110/most-app/internal/docsbot"
)

type FakeRepo struct {
	mu              sync.Mutex                  // guards every field below; fakes are used by parallel tests
	Folders         map[docsbot.Category]string // cached Drive folder ids by category
	Docs            map[docsbot.DocumentID]docsbot.Document
	seq             int
	lastCreated     time.Time
	Deleted         []docsbot.UserID                 // users passed to DeleteUserData
	Updated         map[docsbot.DocumentID]time.Time // last update time, set by tests for the sweeper
	InlineOff       map[docsbot.UserID]bool          // users who turned inline mode off
	InlineErr       error                            // returned by InlineEnabled when set
	Status          *FakeStatus                      // set by NewDeps
	Events          []docsbot.Event                  // recorded product events, in order
	EventsErr       error                            // returned by Record and RecordStart when set
	CountSavedErr   error                            // returned by CountSaved when set
	NumberErr       error                            // returned by NumberPendingAlbum when set
	Started         map[docsbot.UserID]bool          // users with a recorded start event
	Feedbacks       []FeedbackEntry                  // recorded feedback requests, in order
	FeedbackTgUsers map[docsbot.UserID]int64         // Telegram ids by user, for FeedbackAuthor
	FeedbackErr     error                            // returned by the Feedback port methods when set
}

var _ docsbot.Repo = (*FakeRepo)(nil)

// NewFakeRepo returns an empty repository.
func NewFakeRepo() *FakeRepo {
	return &FakeRepo{Docs: map[docsbot.DocumentID]docsbot.Document{}, Started: map[docsbot.UserID]bool{}}
}

// Seed stores a document, assigning an id and creation time if absent.
func (r *FakeRepo) Seed(d docsbot.Document) docsbot.Document {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seedLocked(d)
}

// seedLocked is Seed for callers already holding r.mu.
func (r *FakeRepo) seedLocked(d docsbot.Document) docsbot.Document {
	if d.ID == "" {
		r.seq++
		d.ID = docsbot.DocumentID(fmt.Sprintf("doc-%d", r.seq))
	}
	if d.CreatedAt.IsZero() {
		// Like the column default now(), but strictly increasing so that sorting
		// by creation time is deterministic on a coarse clock.
		now := time.Now()
		if !now.After(r.lastCreated) {
			now = r.lastCreated.Add(time.Nanosecond)
		}
		r.lastCreated = now
		d.CreatedAt = now
	}
	if d.BackupStatus == "" {
		d.BackupStatus = docsbot.BackupNone
	}
	r.Docs[d.ID] = d
	return d
}

func (r *FakeRepo) CreateDocument(_ context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seedLocked(docsbot.Document{
		UserID: in.UserID, Status: docsbot.StatusPending, Kind: in.Kind, Title: in.Title,
		TgFileID: in.FileID, TgFileUniqueID: in.FileUniqueID, BackupStatus: docsbot.BackupNone,
		MediaGroupID: in.MediaGroupID, FileSize: in.FileSize, PageCount: in.PageCount, Width: in.Width, Height: in.Height, MIME: in.MIME,
	}), nil
}

func (r *FakeRepo) DocumentByFileUniqueID(_ context.Context, user docsbot.UserID, uid string) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.Docs {
		if d.UserID == user && d.TgFileUniqueID == uid {
			return d, nil
		}
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *FakeRepo) DocumentByID(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.Docs[id]; ok && d.UserID == user {
		return d, nil
	}
	return docsbot.Document{}, docsbot.ErrNotFound
}

func (r *FakeRepo) PendingByMediaGroup(_ context.Context, user docsbot.UserID, group string) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docsbot.Document
	for _, d := range r.Docs {
		if d.UserID == user && d.MediaGroupID == group && d.Status == docsbot.StatusPending {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (r *FakeRepo) NumberPendingAlbum(_ context.Context, user docsbot.UserID, group, base string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.NumberErr != nil {
		return r.NumberErr
	}
	var ids []docsbot.DocumentID
	for id, d := range r.Docs {
		if d.UserID == user && d.MediaGroupID == group && d.Status == docsbot.StatusPending {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := r.Docs[ids[i]], r.Docs[ids[j]]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	for n, id := range ids {
		d := r.Docs[id]
		d.Title = fmt.Sprintf("%s (%d)", base, n+1)
		r.Docs[id] = d
	}
	return nil
}

func (r *FakeRepo) IntakeStats(_ context.Context, user docsbot.UserID, since time.Time) (docsbot.IntakeStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var st docsbot.IntakeStats
	for _, d := range r.Docs {
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

func (r *FakeRepo) PurgeStalePending(_ context.Context, olderThan time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id, d := range r.Docs {
		if d.Status == docsbot.StatusPending && d.CreatedAt.Before(olderThan) {
			delete(r.Docs, id)
			n++
		}
	}
	return n, nil
}

func (r *FakeRepo) ResetPendingUploads(_ context.Context, user docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, d := range r.Docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved &&
			(d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading) {
			d.BackupStatus = docsbot.BackupNone
			r.Docs[id] = d
		}
	}
	return nil
}

func (r *FakeRepo) MarkUploading(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	return r.update(user, id, func(d *docsbot.Document) { d.BackupStatus = docsbot.BackupUploading })
}

func (r *FakeRepo) SetBackupStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	return r.update(user, id, func(d *docsbot.Document) { d.BackupStatus, d.BackupReason = s, "" })
}

func (r *FakeRepo) FailBackup(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, reason docsbot.BackupReason) error {
	return r.update(user, id, func(d *docsbot.Document) { d.BackupStatus, d.BackupReason = docsbot.BackupFailed, reason })
}

// update applies fn to the user's document under the lock. Like the real
// user-scoped UPDATE, a missing or foreign document is a silent no-op.
func (r *FakeRepo) update(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.Docs[id]; ok && d.UserID == user {
		fn(&d)
		r.Docs[id] = d
	}
	return nil
}

// ClaimFailed mirrors ClaimFailedForRetry: failed (or backed up and missing) -> pending, once.
func (r *FakeRepo) ClaimFailed(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Docs[id]
	missing := d.BackupStatus == docsbot.BackupBackedUp && d.SyncStatus == docsbot.SyncMissing
	if !ok || d.UserID != user || (d.BackupStatus != docsbot.BackupFailed && !missing) {
		return false, nil
	}
	if missing {
		d.DriveFileID = ""
	}
	d.BackupStatus, d.SyncStatus, d.SyncReason = docsbot.BackupPending, docsbot.SyncOK, ""
	r.Docs[id] = d
	return true, nil
}

// StuckUploads mirrors the real query, assuming every user has an active
// integration (the join is covered by the integration tests).
func (r *FakeRepo) StuckUploads(_ context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docsbot.StuckUpload
	for id, d := range r.Docs {
		bs := d.BackupStatus
		if d.Status != docsbot.StatusSaved || (bs != docsbot.BackupPending && bs != docsbot.BackupUploading) {
			continue
		}
		if at, ok := r.Updated[id]; ok && at.Before(olderThan) {
			out = append(out, docsbot.StuckUpload{User: d.UserID, ID: id, UpdatedAt: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// StuckSyncs mirrors the real query like StuckUploads.
func (r *FakeRepo) StuckSyncs(_ context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docsbot.StuckUpload
	for id, d := range r.Docs {
		if d.Status != docsbot.StatusSaved || d.BackupStatus != docsbot.BackupBackedUp || d.SyncStatus != docsbot.SyncPending {
			continue
		}
		if at, ok := r.Updated[id]; ok && at.Before(olderThan) {
			out = append(out, docsbot.StuckUpload{User: d.UserID, ID: id, UpdatedAt: at})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.Before(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *FakeRepo) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	return r.mutate(user, id, func(d *docsbot.Document) { d.Title = title; d.MetaVersion++ })
}

// mutate is update that reports ErrNotFound for a missing or foreign document,
// like the :execrows queries.
func (r *FakeRepo) mutate(user docsbot.UserID, id docsbot.DocumentID, fn func(*docsbot.Document)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Docs[id]
	if !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	fn(&d)
	r.Docs[id] = d
	return nil
}

func (r *FakeRepo) TitleExistsInCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.Docs {
		if d.UserID == user && d.Category == cat && d.Status == docsbot.StatusSaved && d.Title == title {
			return true, nil
		}
	}
	return false, nil
}

func (r *FakeRepo) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.Docs[id]; !ok || d.UserID != user {
		return docsbot.ErrNotFound
	}
	delete(r.Docs, id)
	return nil
}

// SavedFor lists the user's saved documents. It does not lock: the fake's own
// methods call it under mu, a test may call it once no handler is running.
func (r *FakeRepo) SavedFor(user docsbot.UserID) []docsbot.Document {
	var out []docsbot.Document
	for _, d := range r.Docs {
		if d.UserID == user && d.Status == docsbot.StatusSaved {
			out = append(out, d)
		}
	}
	return out
}

func (r *FakeRepo) CountSaved(_ context.Context, user docsbot.UserID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.CountSavedErr != nil {
		return 0, r.CountSavedErr
	}
	return len(r.SavedFor(user)), nil
}

func (r *FakeRepo) CountNotBackedUp(_ context.Context, user docsbot.UserID) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.SavedFor(user) {
		if !d.InDrive() && d.BackupStatus != docsbot.BackupTooLarge { // mirrors the SQL
			n++
		}
	}
	return n, nil
}

func (r *FakeRepo) CategoryCounts(_ context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byCat := map[docsbot.Category]int{}
	for _, d := range r.SavedFor(user) {
		byCat[d.Category]++
	}
	var out []docsbot.CategoryCount
	for c, n := range byCat { // raw and unordered, like the SQL: the service folds and sorts
		out = append(out, docsbot.CategoryCount{Category: c, Count: n})
	}
	return out, nil
}

func (r *FakeRepo) ListByCategory(_ context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []docsbot.Document
	for _, d := range r.SavedFor(user) {
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
