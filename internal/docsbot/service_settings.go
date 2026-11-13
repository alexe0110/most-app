package docsbot

import (
	"context"
	"fmt"
)

// ResetDriveFolders clears the user's cached Drive folder ids, so folders are
// found or created afresh (the Drive may now belong to another Google account).
func (s *Service) ResetDriveFolders(ctx context.Context, user UserID) error {
	if err := s.repo.DeleteAllFolders(ctx, user); err != nil {
		return fmt.Errorf("delete all folders: %w", err)
	}
	return nil
}

// DriveConnected reports whether the user's Drive is connected.
func (s *Service) DriveConnected(ctx context.Context, user UserID) (bool, error) {
	ok, err := s.drive.Connected(ctx, user)
	if err != nil {
		return false, fmt.Errorf("drive status: %w", err)
	}
	return ok, nil
}

// Settings reports the Drive connection and the user's document counts for /settings.
func (s *Service) Settings(ctx context.Context, user UserID) (SettingsView, error) {
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("drive status: %w", err)
	}
	counts, err := s.repo.BackupStatusCounts(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("backup status counts: %w", err)
	}
	inline, err := s.repo.InlineEnabled(ctx, user)
	if err != nil {
		return SettingsView{}, fmt.Errorf("inline enabled: %w", err)
	}
	view := SettingsView{DriveConnected: connected, InlineEnabled: inline}
	for _, c := range counts {
		view.Total += c.Count
		switch c.Status {
		case BackupBackedUp:
			view.InDrive += c.Count
		case BackupFailed:
			view.Failed += c.Count
		}
	}
	view.OnlyTelegram = view.Total - view.InDrive - view.Failed
	return view, nil
}

// Disconnect turns Drive off at the user's request, cancels the user's pending
// uploads (sync and trash jobs stay: their workers see the disconnect and settle
// the document) and resets stuck documents back to Telegram-only. Google failures are
// already swallowed below this layer, so a Drive error means the local
// integration status was not updated (still active): it is returned before
// anything is cancelled.
func (s *Service) Disconnect(ctx context.Context, user UserID) error {
	if err := s.drive.Disconnect(ctx, user); err != nil {
		return fmt.Errorf("disconnect drive: %w", err)
	}
	if err := s.uploads.CancelUploads(ctx, user); err != nil {
		return fmt.Errorf("cancel uploads: %w", err)
	}
	if err := s.repo.ResetPendingUploads(ctx, user); err != nil {
		return fmt.Errorf("reset pending uploads: %w", err)
	}
	return nil
}

// DeleteAllData revokes Drive access, cancels all pending jobs and deletes all of
// the user's data, then soft-deletes the user. A failing revoke does not block
// the deletion: it is reported afterwards as ErrRevokeIncomplete, which callers
// treat as success.
func (s *Service) DeleteAllData(ctx context.Context, user UserID) error {
	revokeErr := s.drive.Revoke(ctx, user)
	if err := s.uploads.CancelUserJobs(ctx, user); err != nil {
		return fmt.Errorf("cancel jobs: %w", err)
	}
	if err := s.repo.DeleteUserData(ctx, user); err != nil {
		return fmt.Errorf("delete user data: %w", err)
	}
	if revokeErr != nil {
		return fmt.Errorf("revoke drive access: %w: %w", ErrRevokeIncomplete, revokeErr)
	}
	return nil
}

// ExportAll returns the user's saved documents (capped at exportLimit) to resend.
func (s *Service) ExportAll(ctx context.Context, user UserID) (Export, error) {
	total, err := s.repo.CountSaved(ctx, user)
	if err != nil {
		return Export{}, fmt.Errorf("count saved: %w", err)
	}
	docs, err := s.repo.SavedForExport(ctx, user, exportLimit)
	if err != nil {
		return Export{}, fmt.Errorf("list for export: %w", err)
	}
	return Export{Docs: docs, Total: total, Truncated: total > exportLimit}, nil
}
