package docsbot

import (
	"context"
	"errors"
	"fmt"
)

// BackupResult is the outcome of a Drive upload attempt.
type BackupResult struct {
	Doc      Document
	TooLarge bool
	// Noop true means the worker should finish silently: there is nothing to do
	// (document gone or already backed up) and the user is not notified.
	Noop bool
	// OrphanTrashErr is set when the document was deleted mid-upload and trashing
	// the uploaded file failed: the file stays in Drive as an orphan.
	OrphanTrashErr error
}

// Backup uploads a saved document to the user's Drive. It is the upload_document
// worker's use case and is idempotent: an already backed-up document is a no-op.
// A file above the download limit is marked too_large and left in Telegram; a
// revoked grant returns ErrDriveRevoked and a full Drive ErrDriveFull; transient errors are returned for retry.
func (s *Service) Backup(ctx context.Context, user UserID, id DocumentID) (BackupResult, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if errors.Is(err, ErrNotFound) {
		// Deleted while the job was queued: finish without retries.
		return BackupResult{Noop: true}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("load document: %w", err)
	}
	if doc.BackupStatus == BackupBackedUp {
		return BackupResult{Doc: doc, Noop: true}, nil
	}
	startVersion := doc.MetaVersion
	// Do not bump updated_at here: a poisoned document that crashes every
	// attempt must still age out to failed via the sweeper's 24h backstop.
	if err := s.repo.MarkUploading(ctx, user, id); err != nil {
		return BackupResult{}, fmt.Errorf("mark uploading: %w", err)
	}

	data, mime, err := s.files.Download(ctx, doc.TgFileID)
	if errors.Is(err, ErrFileTooLarge) {
		if err := s.repo.SetBackupStatus(ctx, user, id, BackupTooLarge); err != nil {
			return BackupResult{}, fmt.Errorf("mark too large: %w", err)
		}
		doc.BackupStatus = BackupTooLarge
		return BackupResult{Doc: doc, TooLarge: true}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("download file: %w", err)
	}

	pages := s.countPages(ctx, doc, mime, data)
	name := RemoteName(doc.Title, doc.MIME, doc.Kind)
	file, err := s.drive.Upload(ctx, user, DriveUpload{Category: doc.Category, Name: name, MIME: mime, Data: data, DocID: string(id)})
	if errors.Is(err, ErrDriveDisconnected) {
		// User disconnected Drive mid-upload: drop back to Telegram-only, finish silently.
		if serr := s.repo.SetBackupStatus(ctx, user, id, BackupNone); serr != nil {
			return BackupResult{}, fmt.Errorf("reset backup status: %w", serr)
		}
		return BackupResult{Noop: true}, nil
	}
	if errors.Is(err, ErrDriveRevoked) {
		return BackupResult{}, ErrDriveRevoked
	}
	if errors.Is(err, ErrDriveFull) {
		return BackupResult{}, ErrDriveFull
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("upload to drive: %w", err)
	}

	queued, err := s.syncs.MarkBackedUp(ctx, user, id, file.ID, startVersion, pages)
	if errors.Is(err, ErrNotFound) {
		// Deleted while the file was uploading: nobody references the file, so
		// trash it (best effort; a failure leaves an orphan the user can delete
		// and is reported to the worker to log).
		return BackupResult{Noop: true, OrphanTrashErr: s.drive.Trash(ctx, user, file.ID)}, nil
	}
	if err != nil {
		return BackupResult{}, fmt.Errorf("mark backed up: %w", err)
	}
	doc.BackupStatus = BackupBackedUp
	doc.DriveFileID = file.ID
	if pages > 0 {
		doc.PageCount = pages
	}
	if queued {
		doc.SyncStatus = SyncPending
	}
	return BackupResult{Doc: doc}, nil
}

// countPages counts a PDF's pages for the card; 0 means unknown (not a PDF, no
// counter configured, or the file could not be read). A failure is not an error:
// the page count is decoration and must not hold up the upload.
func (s *Service) countPages(ctx context.Context, doc Document, mime string, data []byte) int {
	if s.pages == nil || (doc.MIME != "application/pdf" && mime != "application/pdf") {
		return 0
	}
	n, err := s.pages.PageCount(ctx, data)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// MarkBackupFailed records a terminal upload failure with its reason
// (BackupReason*; "" is stored as unknown and shown as unavailable).
func (s *Service) MarkBackupFailed(ctx context.Context, user UserID, id DocumentID, reason BackupReason) error {
	if err := s.repo.FailBackup(ctx, user, id, reason); err != nil {
		return fmt.Errorf("mark backup failed: %w", err)
	}
	return nil
}

// SweepResult summarizes one sweeper run.
type SweepResult struct {
	Found    int
	Requeued int
	Failed   []StuckUpload
	// SyncsRequeued and SyncsFailed count documents whose cloud sync was stuck.
	SyncsRequeued int
	SyncsFailed   int
}

// SweepStuckUploads requeues uploads stuck mid-flight and fails the ones stuck
// for too long. Requeueing does not touch backup_status/updated_at, so the
// stuck-since clock keeps running; a live job dedups to a no-op.
func (s *Service) SweepStuckUploads(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	now := s.now()
	rows, err := s.repo.StuckUploads(ctx, now.Add(-sweepStaleAfter), sweepBatch)
	if err != nil {
		return res, fmt.Errorf("list stuck uploads: %w", err)
	}
	res.Found = len(rows)
	for _, r := range rows {
		if now.Sub(r.UpdatedAt) >= sweepFailAfter {
			if err := s.repo.FailBackup(ctx, r.User, r.ID, BackupReasonUnavailable); err != nil {
				return res, fmt.Errorf("fail stuck upload: %w", err)
			}
			res.Failed = append(res.Failed, r)
			continue
		}
		if err := s.uploads.RequeueUpload(ctx, r.User, r.ID); err != nil {
			return res, fmt.Errorf("requeue stuck upload: %w", err)
		}
		res.Requeued++
	}
	return s.sweepStuckSyncs(ctx, now, res)
}

// TransferSaved moves every saved document not yet backed up to Drive, tracked by
// one transfer status message that takes over the given Telegram message. ok is
// false when there is nothing to transfer.
func (s *Service) TransferSaved(ctx context.Context, user UserID, chatID int64, messageID int) (StatusView, bool, error) {
	ids, err := s.repo.ClaimTransferable(ctx, user)
	if err != nil {
		return StatusView{}, false, fmt.Errorf("claim not backed up: %w", err)
	}
	return s.startGroupedUpload(ctx, user, chatID, messageID, ids)
}

// NotBackedUpCount reports how many saved documents are not yet on Drive.
func (s *Service) NotBackedUpCount(ctx context.Context, user UserID) (int, error) {
	n, err := s.repo.CountNotBackedUp(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("count not backed up: %w", err)
	}
	return n, nil
}

// RetryBackup re-enqueues a failed document's Drive upload. Any other state
// (done, in flight, already retried) or a foreign document returns ErrNotRetryable.
func (s *Service) RetryBackup(ctx context.Context, user UserID, id DocumentID) error {
	queued, err := s.claimAndRequeue(ctx, user, id)
	if err != nil {
		return err
	}
	if !queued {
		return ErrNotRetryable
	}
	return nil
}

// claimAndRequeue atomically moves a failed document to pending and queues its
// upload. queued is false when the document was not failed (nothing is done).
func (s *Service) claimAndRequeue(ctx context.Context, user UserID, id DocumentID) (queued bool, err error) {
	claimed, err := s.repo.ClaimFailed(ctx, user, id)
	if err != nil {
		return false, fmt.Errorf("claim failed upload: %w", err)
	}
	if !claimed {
		return false, nil
	}
	if err := s.uploads.RequeueUpload(ctx, user, id); err != nil {
		return false, fmt.Errorf("requeue upload: %w", err)
	}
	return true, nil
}

// RetryFailed re-queues every failed Drive upload under one transfer status
// message that takes over the given Telegram message. ok is false when nothing failed.
func (s *Service) RetryFailed(ctx context.Context, user UserID, chatID int64, messageID int) (StatusView, bool, error) {
	ids, err := s.repo.ClaimAllFailed(ctx, user)
	if err != nil {
		return StatusView{}, false, fmt.Errorf("claim failed: %w", err)
	}
	return s.startGroupedUpload(ctx, user, chatID, messageID, ids)
}
