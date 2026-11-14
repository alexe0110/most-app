package docsbottest

import (
	"context"
	"sync"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// FakeUploads records enqueued uploads and, like the real transaction, saves the
// chosen category together with the backup status.
type FakeUploads struct {
	mu         sync.Mutex
	Repo       *FakeRepo
	Calls      []docsbot.DocumentID
	Cancels    []docsbot.UserID // CancelUploads calls
	AllJobs    []docsbot.UserID // CancelUserJobs calls
	Requeued   []docsbot.DocumentID
	EnqueueErr error // returned by SaveChosen for a queued upload before anything changes, like a failed tx
}

// SaveChosen mirrors the transaction: it either applies the whole choice or, on
// EnqueueErr for a queued upload, changes nothing.
func (u *FakeUploads) SaveChosen(_ context.Context, c docsbot.SaveChoice) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.EnqueueErr != nil && c.Backup == docsbot.BackupPending {
		return false, u.EnqueueErr
	}
	if u.Repo == nil {
		return true, nil // a bare FakeUploads accepts every choice and changes nothing
	}
	u.Repo.mu.Lock()
	defer u.Repo.mu.Unlock()
	d, ok := u.Repo.Docs[c.ID]
	if !ok || d.UserID != c.User || d.Status != docsbot.StatusPending {
		return false, nil
	}
	d.Category, d.Status = c.Category, docsbot.StatusSaved
	if c.Title != "" {
		d.Title = c.Title
		d.MetaVersion++
	}
	if c.Backup != docsbot.BackupNone {
		d.BackupStatus = c.Backup
	}
	if c.Backup == docsbot.BackupPending {
		u.Calls = append(u.Calls, c.ID)
	}
	u.Repo.Docs[c.ID] = d
	return true, nil
}

// RequeueUpload records the call; like the real one it leaves backup_status alone.
func (u *FakeUploads) RequeueUpload(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Requeued = append(u.Requeued, id)
	return nil
}

func (u *FakeUploads) CancelUserJobs(_ context.Context, user docsbot.UserID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.AllJobs = append(u.AllJobs, user)
	return nil
}

func (u *FakeUploads) CancelUploads(_ context.Context, user docsbot.UserID) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.Cancels = append(u.Cancels, user)
	return nil
}

// FakeSyncs is a docsbot.Syncs over FakeRepo that mirrors the SQL decisions:
// a backed-up document with a live file goes sync-pending and a job is recorded.
type FakeSyncs struct {
	mu      sync.Mutex
	Repo    *FakeRepo
	Jobs    []docsbot.DocumentID // sync jobs enqueued
	Trashes []string             // remote ids queued for trashing
	Err     error                // returned by every method when set
}

var _ docsbot.Syncs = (*FakeSyncs)(nil)

func (s *FakeSyncs) change(user docsbot.UserID, id docsbot.DocumentID, cloud bool, fn func(*docsbot.Document)) (bool, error) {
	if s.Err != nil {
		return false, s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	fn(&d)
	d.MetaVersion++
	queued := cloud && d.BackupStatus == docsbot.BackupBackedUp && d.SyncStatus != docsbot.SyncMissing
	if queued {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.Repo.Docs[id] = d
	if queued {
		s.addJob(id)
	}
	return queued, nil
}

func (s *FakeSyncs) addJob(id docsbot.DocumentID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Jobs = append(s.Jobs, id)
}

func (s *FakeSyncs) SetTitle(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, title string, cloud bool) (bool, error) {
	return s.change(user, id, cloud, func(d *docsbot.Document) { d.Title = title })
}

func (s *FakeSyncs) SetCategory(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category, cloud bool) (bool, error) {
	return s.change(user, id, cloud, func(d *docsbot.Document) { d.Category = cat })
}

func (s *FakeSyncs) EnqueueSync(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	if s.Err != nil {
		return s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
	if !ok || d.UserID != user || d.BackupStatus != docsbot.BackupBackedUp || d.SyncStatus == docsbot.SyncMissing {
		return nil
	}
	d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	d.MetaVersion++ // like MarkSyncPending: a stale parallel MarkSynced must lose
	s.Repo.Docs[id] = d
	s.addJob(id)
	return nil
}

func (s *FakeSyncs) MarkBackedUp(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string, startVersion, pages int) (bool, error) {
	if s.Err != nil {
		return false, s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
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
	s.Repo.Docs[id] = d
	if queued {
		s.addJob(id)
	}
	return queued, nil
}

// ReplaceContent mirrors ReplaceDocumentContent: the guarded update, the unique
// (user, file) constraint, and the sync job for a backed-up document.
func (s *FakeSyncs) ReplaceContent(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, in docsbot.IncomingFile, cloud bool) (bool, error) {
	if s.Err != nil {
		return false, s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
	if !ok || d.UserID != user ||
		d.BackupStatus == docsbot.BackupPending || d.BackupStatus == docsbot.BackupUploading ||
		d.SyncStatus == docsbot.SyncPending || d.SyncStatus == docsbot.SyncMissing {
		return false, nil
	}
	for _, o := range s.Repo.Docs {
		if o.ID != id && o.UserID == user && o.TgFileUniqueID == in.FileUniqueID {
			return false, docsbot.ErrDuplicateFile
		}
	}
	d.Kind, d.TgFileID, d.TgFileUniqueID, d.FileSize, d.MIME, d.PageCount = in.Kind, in.FileID, in.FileUniqueID, in.FileSize, in.MIME, in.PageCount
	d.Width, d.Height = in.Width, in.Height
	d.MetaVersion++
	backedUp := cloud && d.BackupStatus == docsbot.BackupBackedUp
	d.ContentDirty = backedUp
	if backedUp {
		d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	}
	s.Repo.Docs[id] = d
	if backedUp {
		s.addJob(id)
	}
	return true, nil
}

func (s *FakeSyncs) RetrySync(_ context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	if s.Err != nil {
		return false, s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
	if !ok || d.UserID != user || d.BackupStatus != docsbot.BackupBackedUp || d.SyncStatus != docsbot.SyncFailed {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = docsbot.SyncPending, ""
	s.Repo.Docs[id] = d
	s.addJob(id)
	return true, nil
}

func (s *FakeSyncs) DeleteDocument(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, cloud bool) (bool, error) {
	if s.Err != nil {
		return false, s.Err
	}
	s.Repo.mu.Lock()
	defer s.Repo.mu.Unlock()
	d, ok := s.Repo.Docs[id]
	if !ok || d.UserID != user {
		return false, docsbot.ErrNotFound
	}
	delete(s.Repo.Docs, id)
	if d.BackupStatus != docsbot.BackupBackedUp || d.DriveFileID == "" || d.SyncStatus == docsbot.SyncMissing {
		return false, nil
	}
	if !cloud {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Trashes = append(s.Trashes, d.DriveFileID)
	return false, nil
}

func (s *FakeSyncs) RequeueSync(_ context.Context, _ docsbot.UserID, id docsbot.DocumentID) error {
	if s.Err != nil {
		return s.Err
	}
	s.addJob(id)
	return nil
}
