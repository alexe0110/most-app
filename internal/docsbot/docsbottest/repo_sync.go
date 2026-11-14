package docsbottest

import (
	"context"
	"slices"
	"sort"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// MarkSynced mirrors MarkSynced: only a pending sync at the same meta_version.
func (r *FakeRepo) MarkSynced(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, version int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Docs[id]
	if !ok || d.UserID != user || d.MetaVersion != version || d.SyncStatus != docsbot.SyncPending {
		return false, nil
	}
	d.SyncStatus, d.SyncReason, d.ContentDirty = docsbot.SyncOK, "", false
	r.Docs[id] = d
	return true, nil
}

// TransitionSyncStatus mirrors the conditional UPDATE.
func (r *FakeRepo) TransitionSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, remoteID string, from, to docsbot.SyncStatus) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.Docs[id]
	if !ok || d.UserID != user || d.SyncStatus != from || d.BackupStatus != docsbot.BackupBackedUp || d.DriveFileID != remoteID {
		return false, nil
	}
	d.SyncStatus, d.SyncReason = to, ""
	r.Docs[id] = d
	return true, nil
}

func (r *FakeRepo) SetSyncStatus(_ context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.SyncStatus, reason string) error {
	return r.update(user, id, func(d *docsbot.Document) { d.SyncStatus, d.SyncReason = s, reason })
}

// ClaimTransferable mirrors ClaimTransferableDocuments: none/failed -> pending, atomically.
func (r *FakeRepo) ClaimTransferable(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupNone, docsbot.BackupFailed), nil
}

// ClaimAllFailed mirrors ClaimFailedDocuments: failed -> pending, atomically.
func (r *FakeRepo) ClaimAllFailed(_ context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	return r.claim(user, docsbot.BackupFailed), nil
}

// claim atomically (under the lock) moves the user's saved documents in one of
// the given states to pending and returns their ids, oldest first.
func (r *FakeRepo) claim(user docsbot.UserID, from ...docsbot.BackupStatus) []docsbot.DocumentID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var docs []docsbot.Document
	for _, d := range r.SavedFor(user) {
		if slices.Contains(from, d.BackupStatus) {
			docs = append(docs, d)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	out := make([]docsbot.DocumentID, 0, len(docs))
	for _, d := range docs {
		d.BackupStatus = docsbot.BackupPending
		r.Docs[d.ID] = d
		out = append(out, d.ID)
	}
	return out
}

func (r *FakeRepo) BackupStatusCounts(_ context.Context, user docsbot.UserID) ([]docsbot.BackupStatusCount, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	byStatus := map[docsbot.BackupStatus]int{}
	for _, d := range r.SavedFor(user) {
		byStatus[d.BackupStatus]++
	}
	out := make([]docsbot.BackupStatusCount, 0, len(byStatus))
	for st, n := range byStatus {
		out = append(out, docsbot.BackupStatusCount{Status: st, Count: n})
	}
	return out, nil
}

func (r *FakeRepo) SavedForExport(_ context.Context, user docsbot.UserID, limit int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	docs := r.SavedFor(user)
	sort.Slice(docs, func(i, j int) bool { return docs[i].CreatedAt.Before(docs[j].CreatedAt) })
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

func (r *FakeRepo) DeleteUserData(_ context.Context, user docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, d := range r.Docs {
		if d.UserID == user {
			delete(r.Docs, id)
		}
	}
	r.Deleted = append(r.Deleted, user)
	return nil
}

var _ docsbot.Folders = (*FakeRepo)(nil)

func (r *FakeRepo) CategoryFolderID(_ context.Context, _ docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.Folders[cat]
	return id, ok, nil
}

func (r *FakeRepo) DeleteAllFolders(_ context.Context, _ docsbot.UserID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.Folders)
	return nil
}
