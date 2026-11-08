package docsbot

import (
	"context"
	"fmt"
)

// StatusTarget returns the user's open status message if it is still inside the
// grouping window and can absorb another document.
func (s *Service) StatusTarget(ctx context.Context, user UserID) (StatusMessage, bool, error) {
	sm, ok, err := s.status.OpenForUser(ctx, user)
	if err != nil {
		return StatusMessage{}, false, fmt.Errorf("open status message: %w", err)
	}
	if !ok || sm.Closed || s.now().Sub(sm.CreatedAt) >= statusWindow {
		return StatusMessage{}, false, nil
	}
	return sm, true, nil
}

// OpenStatus records a new status message and attaches the document to it.
func (s *Service) OpenStatus(ctx context.Context, user UserID, chatID int64, messageID int, doc DocumentID) (StatusMessage, error) {
	sm, err := s.status.Create(ctx, user, chatID, messageID)
	if err != nil {
		return StatusMessage{}, fmt.Errorf("create status message: %w", err)
	}
	if err := s.status.Attach(ctx, doc, sm.ID); err != nil {
		return StatusMessage{}, fmt.Errorf("attach document: %w", err)
	}
	return sm, nil
}

// AttachStatus adds the document to an existing status message.
func (s *Service) AttachStatus(ctx context.Context, doc DocumentID, id StatusMessageID) error {
	if err := s.status.Attach(ctx, doc, id); err != nil {
		return fmt.Errorf("attach document: %w", err)
	}
	return nil
}

// CloseStatus stops a status message from absorbing further documents.
func (s *Service) CloseStatus(ctx context.Context, id StatusMessageID) error {
	if err := s.status.Close(ctx, id); err != nil {
		return fmt.Errorf("close status message: %w", err)
	}
	return nil
}

// StatusView loads what is needed to render the document's status message. A
// document outside any status message yields a non-grouped view.
func (s *Service) StatusView(ctx context.Context, user UserID, doc DocumentID) (StatusView, error) {
	d, err := s.repo.DocumentByID(ctx, user, doc)
	if err != nil {
		return StatusView{}, fmt.Errorf("load document: %w", err)
	}
	if d.StatusMessageID == "" {
		return StatusView{Doc: d}, nil
	}
	v, err := s.StatusViewByID(ctx, user, d.StatusMessageID)
	if err != nil {
		return StatusView{}, err
	}
	v.Doc = d
	return v, nil
}

// StatusViewByID loads a status message with its documents. A message that
// belongs to another user is reported as ErrNotFound.
func (s *Service) StatusViewByID(ctx context.Context, user UserID, id StatusMessageID) (StatusView, error) {
	sm, err := s.status.ByID(ctx, id)
	if err != nil {
		return StatusView{}, fmt.Errorf("load status message: %w", err)
	}
	if sm.UserID != user {
		return StatusView{}, ErrNotFound
	}
	lines, err := s.status.DocumentsIn(ctx, id)
	if err != nil {
		return StatusView{}, fmt.Errorf("list status documents: %w", err)
	}
	folders := make(map[Category]string)
	allTerminal := true
	for _, l := range lines {
		if !l.BackupStatus.Terminal() {
			allTerminal = false
		}
		if _, seen := folders[l.Category]; seen || l.Category == "" {
			continue
		}
		fid, ok, err := s.folders.CategoryFolderID(ctx, user, l.Category)
		if err != nil {
			return StatusView{}, fmt.Errorf("category folder: %w", err)
		}
		if ok {
			folders[l.Category] = fid
		}
	}
	return StatusView{Grouped: true, Message: sm, Lines: lines, FolderByCat: folders, AllTerminal: allTerminal}, nil
}

// SetStatusMessageID records the new Telegram message id after the status
// message was re-sent.
func (s *Service) SetStatusMessageID(ctx context.Context, id StatusMessageID, messageID int) error {
	if err := s.status.SetMessageID(ctx, id, messageID); err != nil {
		return fmt.Errorf("set status message id: %w", err)
	}
	return nil
}

// RetryStatusFailed re-queues every failed upload of the user's status message
// and returns how many were queued.
func (s *Service) RetryStatusFailed(ctx context.Context, user UserID, id StatusMessageID) (int, error) {
	v, err := s.StatusViewByID(ctx, user, id)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range v.Lines {
		if d.BackupStatus != BackupFailed {
			continue
		}
		if err := s.uploads.EnqueueUpload(ctx, user, d.ID); err != nil {
			return n, fmt.Errorf("enqueue upload: %w", err)
		}
		n++
	}
	return n, nil
}
