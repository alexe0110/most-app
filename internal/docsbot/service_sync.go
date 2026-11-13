package docsbot

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SyncResult is the outcome of a cloud sync attempt.
type SyncResult struct {
	Doc Document
	// Noop true means there was nothing to do (document gone, not backed up, or
	// already in sync); the worker finishes silently.
	Noop bool
	// Missing true means the Drive file is gone; the document is now sync_status
	// missing and the user can re-upload it.
	Missing bool
}

// SyncRemote makes the cloud file match the document row: content (when dirty),
// category folder and name. It is the storage_sync worker's use case, idempotent
// and safe to retry. A revoked grant returns ErrDriveRevoked; other errors are
// returned for retry.
func (s *Service) SyncRemote(ctx context.Context, user UserID, id DocumentID) (SyncResult, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		return SyncResult{Noop: true}, nil
	}
	if err != nil {
		return SyncResult{}, fmt.Errorf("load document: %w", err)
	}
	if doc.BackupStatus != BackupBackedUp || doc.DriveFileID == "" || doc.SyncStatus == SyncMissing ||
		(doc.SyncStatus == SyncOK && !doc.ContentDirty) {
		return SyncResult{Doc: doc, Noop: true}, nil
	}

	err = s.pushToRemote(ctx, user, doc)
	switch {
	case errors.Is(err, ErrRemoteMissing):
		if serr := s.repo.SetSyncStatus(ctx, user, id, SyncMissing, ""); serr != nil {
			return SyncResult{}, fmt.Errorf("mark sync missing: %w", serr)
		}
		doc.SyncStatus = SyncMissing
		return SyncResult{Doc: doc, Missing: true}, nil
	case errors.Is(err, ErrDriveRevoked):
		return SyncResult{}, ErrDriveRevoked
	case errors.Is(err, ErrDriveDisconnected):
		if serr := s.repo.SetSyncStatus(ctx, user, id, SyncFailed, ""); serr != nil {
			return SyncResult{}, fmt.Errorf("mark sync failed: %w", serr)
		}
		doc.SyncStatus = SyncFailed
		return SyncResult{Doc: doc, Noop: true}, nil
	case err != nil:
		return SyncResult{}, err
	}

	won, err := s.repo.MarkSynced(ctx, user, id, doc.MetaVersion)
	if err != nil {
		return SyncResult{}, fmt.Errorf("mark synced: %w", err)
	}
	if won {
		doc.SyncStatus, doc.ContentDirty = SyncOK, false
		return SyncResult{Doc: doc}, nil
	}
	// The row changed while we worked: run again for the newer state, unless the
	// document is gone or no longer backed up.
	cur, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		return SyncResult{Doc: doc, Noop: true}, nil
	}
	if err != nil {
		return SyncResult{}, fmt.Errorf("reload document: %w", err)
	}
	if cur.BackupStatus == BackupBackedUp {
		if err := s.syncs.EnqueueSync(ctx, user, id); err != nil {
			return SyncResult{}, fmt.Errorf("enqueue sync: %w", err)
		}
	}
	return SyncResult{Doc: cur}, nil
}

// pushToRemote applies the document's content, folder and name to the remote file.
func (s *Service) pushToRemote(ctx context.Context, user UserID, doc Document) error {
	if doc.ContentDirty {
		data, mime, err := s.files.Download(ctx, doc.TgFileID)
		if err != nil {
			return fmt.Errorf("download file: %w", err)
		}
		if err := s.drive.Replace(ctx, user, doc.DriveFileID, mime, data); err != nil {
			return fmt.Errorf("replace remote content: %w", err)
		}
	}
	if err := s.drive.Move(ctx, user, doc.DriveFileID, doc.Category); err != nil {
		return fmt.Errorf("move remote file: %w", err)
	}
	name := RemoteName(doc.Title, doc.MIME, doc.Kind)
	if err := s.drive.Rename(ctx, user, doc.DriveFileID, name); err != nil {
		return fmt.Errorf("rename remote file: %w", err)
	}
	return nil
}

// MarkSyncFailed records a terminal sync failure; reason is "" or SyncReasonRevoked.
func (s *Service) MarkSyncFailed(ctx context.Context, user UserID, id DocumentID, reason string) error {
	if err := s.repo.SetSyncStatus(ctx, user, id, SyncFailed, reason); err != nil {
		return fmt.Errorf("mark sync failed: %w", err)
	}
	return nil
}

// RetrySync re-queues a failed sync. Any other state or a foreign document
// returns ErrNotRetryable.
func (s *Service) RetrySync(ctx context.Context, user UserID, id DocumentID) error {
	queued, err := s.syncs.RetrySync(ctx, user, id)
	if err != nil {
		return fmt.Errorf("retry sync: %w", err)
	}
	if !queued {
		return ErrNotRetryable
	}
	return nil
}

// TrashRemote moves a deleted document's cloud file to the trash. It is the
// storage_trash worker's use case; a file that is already gone counts as done.
func (s *Service) TrashRemote(ctx context.Context, user UserID, remoteID string) error {
	err := s.drive.Trash(ctx, user, remoteID)
	if err != nil && !errors.Is(err, ErrRemoteMissing) {
		return err
	}
	return nil
}

// sweepStuckSyncs recovers syncs left pending (a worker died, or failing the
// sync failed on the last attempt): requeue them, and fail the ones stuck for too
// long. Requeueing leaves updated_at alone, so the clock keeps running.
func (s *Service) sweepStuckSyncs(ctx context.Context, now time.Time, res SweepResult) (SweepResult, error) {
	rows, err := s.repo.StuckSyncs(ctx, now.Add(-sweepStaleAfter), sweepBatch)
	if err != nil {
		return res, fmt.Errorf("list stuck syncs: %w", err)
	}
	for _, r := range rows {
		if now.Sub(r.UpdatedAt) >= sweepFailAfter {
			if err := s.repo.SetSyncStatus(ctx, r.User, r.ID, SyncFailed, ""); err != nil {
				return res, fmt.Errorf("fail stuck sync: %w", err)
			}
			res.SyncsFailed++
			continue
		}
		if err := s.syncs.RequeueSync(ctx, r.User, r.ID); err != nil {
			return res, fmt.Errorf("requeue stuck sync: %w", err)
		}
		res.SyncsRequeued++
	}
	return res, nil
}
