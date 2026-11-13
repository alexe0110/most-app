package docsbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Receive stores a freshly sent file as pending and asks for a category. A file
// already saved returns AlreadySaved; a still-pending one re-asks the category.
func (s *Service) Receive(ctx context.Context, in IncomingFile) (Reply, error) {
	in.Title = NormalizeTitle(in.Title)
	existing, err := s.repo.DocumentByFileUniqueID(ctx, in.UserID, in.FileUniqueID)
	switch {
	case err == nil && existing.Status == StatusSaved:
		return AlreadySaved{Doc: existing}, nil
	case err == nil:
		return NeedCategory{Doc: existing}, nil
	case !errors.Is(err, ErrNotFound):
		return nil, fmt.Errorf("lookup document: %w", err)
	}

	stats, err := s.repo.IntakeStats(ctx, in.UserID, s.now().Add(-intakeWindow))
	if err != nil {
		return nil, fmt.Errorf("intake stats: %w", err)
	}
	switch {
	case stats.Total >= MaxDocumentsPerUser:
		return QuotaExceeded{}, nil
	case stats.Recent >= maxFilesPerMinute:
		return RateLimited{}, nil
	case stats.Pending >= maxPendingPerUser:
		return TooManyPending{}, nil
	}

	doc, err := s.repo.CreateDocument(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return NeedCategory{Doc: doc}, nil
}

// SaveDerived stores a file made from another document (a compressed copy, a
// PDF) as a new document. When the source is saved, the copy lands in the
// source's category under DerivedTitle(source title, suffix) and the result is
// Saved. A pending or absent source (empty sourceID) has no category to inherit:
// the copy is saved in CatOther under in.Title, and the user can change the
// category from the card. Limit replies and duplicates pass through unchanged;
// an unknown source is ErrNotFound.
func (s *Service) SaveDerived(ctx context.Context, user UserID, sourceID DocumentID, in IncomingFile, suffix string) (Reply, error) {
	in.UserID = user
	var src Document
	fromSaved := false
	if sourceID != "" {
		var err error
		src, err = s.repo.DocumentByID(ctx, user, sourceID)
		if err != nil {
			return nil, fmt.Errorf("load source document: %w", err)
		}
		fromSaved = src.Status == StatusSaved
	}
	if fromSaved {
		in.Title = DerivedTitle(src.Title, suffix)
	}

	rep, err := s.Receive(ctx, in)
	if err != nil {
		return nil, err
	}
	need, ok := rep.(NeedCategory)
	if !ok {
		return rep, nil
	}
	cat := CatOther
	if fromSaved {
		cat = src.Category
	}
	saved, err := s.ChooseCategory(ctx, user, need.Doc.ID, cat)
	if err != nil {
		// Don't leave the copy pending: nobody will categorize it.
		if delErr := s.repo.DeleteDocument(ctx, user, need.Doc.ID); delErr != nil && !errors.Is(delErr, ErrNotFound) {
			return nil, errors.Join(err, fmt.Errorf("discard derived document: %w", delErr))
		}
		return nil, err
	}
	return saved, nil
}

// DiscardAlbum deletes the still-pending documents of an album, e.g. after its
// photos were merged into one PDF. Saved documents are never touched. It returns
// how many were removed; a document purged in parallel is skipped.
func (s *Service) DiscardAlbum(ctx context.Context, user UserID, group string) (int, error) {
	if group == "" {
		return 0, nil
	}
	pending, err := s.repo.PendingByMediaGroup(ctx, user, group)
	if err != nil {
		return 0, fmt.Errorf("list album: %w", err)
	}
	n := 0
	for _, d := range pending {
		err := s.repo.DeleteDocument(ctx, user, d.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("delete album document: %w", err)
		}
		n++
	}
	return n, nil
}

// PurgeStalePending deletes pending documents nobody categorized within the TTL
// and returns how many were removed. It is the purge_pending job's use case.
func (s *Service) PurgeStalePending(ctx context.Context) (int, error) {
	n, err := s.repo.PurgeStalePending(ctx, s.now().Add(-pendingTTL))
	if err != nil {
		return 0, fmt.Errorf("purge stale pending: %w", err)
	}
	return n, nil
}

// ChooseCategoryGroup saves every pending document of the album that anyDocID
// belongs to under cat. The album lives in the database, so it survives a
// restart. A document without an album is saved alone. It is idempotent like
// ChooseCategory: once nothing is pending the result is flagged Already.
func (s *Service) ChooseCategoryGroup(ctx context.Context, user UserID, anyDocID DocumentID, cat Category) (SavedGroup, error) {
	if !cat.Valid() {
		return SavedGroup{}, ErrUnknownCategory
	}
	anchor, err := s.repo.DocumentByID(ctx, user, anyDocID)
	if err != nil {
		return SavedGroup{}, fmt.Errorf("load document: %w", err)
	}
	ids := []DocumentID{anchor.ID}
	if anchor.MediaGroupID != "" {
		pending, err := s.repo.PendingByMediaGroup(ctx, user, anchor.MediaGroupID)
		if err != nil {
			return SavedGroup{}, fmt.Errorf("list album: %w", err)
		}
		if len(pending) > 0 {
			ids = ids[:0]
			for _, d := range pending {
				ids = append(ids, d.ID)
			}
		}
	}
	var out SavedGroup
	for _, id := range ids {
		saved, err := s.ChooseCategory(ctx, user, id, cat)
		if err != nil {
			return out, err
		}
		if saved.Already {
			continue
		}
		out.Docs = append(out.Docs, saved.Doc)
		out.OfferDrive = out.OfferDrive || saved.OfferDrive
	}
	out.Already = len(out.Docs) == 0
	return out, nil
}

// ChooseCategory saves a pending document under cat. It is idempotent: a second
// tap on an already-saved document returns it flagged Already. When Drive is connected the
// document is queued for upload (the upload_document worker performs it); otherwise it stays in
// Telegram and we may offer to connect Drive.
func (s *Service) ChooseCategory(ctx context.Context, user UserID, id DocumentID, cat Category) (Saved, error) {
	if !cat.Valid() {
		return Saved{}, ErrUnknownCategory
	}
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Saved{}, fmt.Errorf("load document: %w", err)
	}
	if doc.Status == StatusSaved {
		// A first tap that saved the document but failed to queue the upload leaves
		// it saved with no backup; a repeated tap is the user's only way to retry.
		if doc.BackupStatus == BackupNone {
			if _, err := s.queueIfConnected(ctx, user, &doc); err != nil {
				return Saved{}, err
			}
		}
		return Saved{Doc: doc, Already: true}, nil
	}

	won, err := s.repo.SaveDocument(ctx, user, id, cat)
	if err != nil {
		return Saved{}, fmt.Errorf("save document: %w", err)
	}
	if !won {
		// Lost a race: a parallel tap saved it, or the purge job deleted it.
		cur, err := s.repo.DocumentByID(ctx, user, id)
		if err != nil {
			return Saved{}, fmt.Errorf("reload document: %w", err)
		}
		return Saved{Doc: cur, Already: true}, nil
	}
	doc.Category = cat
	doc.Status = StatusSaved

	if strings.TrimSpace(doc.Title) == "" {
		title, err := s.defaultTitle(ctx, user, cat)
		if err != nil {
			return Saved{}, err
		}
		if err := s.repo.SetTitle(ctx, user, id, title); err != nil {
			return Saved{}, fmt.Errorf("set default title: %w", err)
		}
		doc.Title = title
	}

	queued, err := s.queueIfConnected(ctx, user, &doc)
	if err != nil {
		return Saved{}, err
	}
	if queued {
		return Saved{Doc: doc}, nil
	}

	count, err := s.repo.CountSaved(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("count saved: %w", err)
	}
	return Saved{Doc: doc, OfferDrive: count == 1 || count == 5}, nil
}

// queueIfConnected backs the saved document up when Drive is connected and
// reports whether it did (or marked it too large); false means Drive is off and
// the document stays in Telegram.
func (s *Service) queueIfConnected(ctx context.Context, user UserID, doc *Document) (bool, error) {
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return false, fmt.Errorf("drive status: %w", err)
	}
	if !connected {
		return false, nil
	}
	return true, s.queueBackup(ctx, user, doc)
}

// queueBackup sets the upload job for a saved document, or marks it too large
// when it is known to exceed the Drive limit, and updates doc to match.
func (s *Service) queueBackup(ctx context.Context, user UserID, doc *Document) error {
	if doc.FileSize > MaxDriveBytes {
		// Known to be above the limit: skip the doomed download, keep it in Telegram.
		if err := s.repo.SetBackupStatus(ctx, user, doc.ID, BackupTooLarge); err != nil {
			return fmt.Errorf("mark too large: %w", err)
		}
		doc.BackupStatus = BackupTooLarge
		return nil
	}
	if err := s.uploads.EnqueueUpload(ctx, user, doc.ID); err != nil {
		return fmt.Errorf("enqueue upload: %w", err)
	}
	doc.BackupStatus = BackupPending
	return nil
}

// defaultTitle builds "<category> — dd.mm.yyyy", adding " (n)" until it is unique
// within the user's category.
func (s *Service) defaultTitle(ctx context.Context, user UserID, cat Category) (string, error) {
	base := cat.Label() + " — " + s.now().In(s.loc).Format("02.01.2006")
	title := base
	for i := 2; i <= maxTitleSuffix; i++ {
		exists, err := s.repo.TitleExistsInCategory(ctx, user, cat, title)
		if err != nil {
			return "", fmt.Errorf("check title collision: %w", err)
		}
		if !exists {
			return title, nil
		}
		title = fmt.Sprintf("%s (%d)", base, i)
	}
	return title, nil // titles are not unique-constrained: accept the last candidate
}

// maxTitleSuffix bounds the default-title collision search.
const maxTitleSuffix = 100

// ReplaceContent swaps the file of a saved document for in (a converted copy of
// it), keeping id, title and category. The user's other document with the same
// file is AlreadySaved and nothing changes; the same file as the current one is
// Replaced unchanged. A backed-up document is queued for a cloud sync; one that
// is not on Drive changes in the database only, and its upload takes the new file.
// A document that is uploading or syncing is ReplaceBusy, one whose Drive file is
// gone ReplaceMissing, an unknown one ErrNotFound.
func (s *Service) ReplaceContent(ctx context.Context, user UserID, id DocumentID, in IncomingFile) (Reply, error) {
	in.UserID = user
	cur, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("load document: %w", err)
	}
	if cur.TgFileUniqueID == in.FileUniqueID {
		return Replaced{Doc: cur}, nil
	}
	if other, err := s.repo.DocumentByFileUniqueID(ctx, user, in.FileUniqueID); err == nil {
		if other.ID == id {
			return Replaced{Doc: other}, nil // a parallel tap already swapped it
		}
		return AlreadySaved{Doc: other}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("lookup document: %w", err)
	}

	// Without Drive only the row changes, like a rename: the Drive file stays old.
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("drive status: %w", err)
	}
	replaced, err := s.syncs.ReplaceContent(ctx, user, id, in, connected)
	if errors.Is(err, ErrDuplicateFile) {
		// Lost a race with a save of the same file.
		other, lerr := s.repo.DocumentByFileUniqueID(ctx, user, in.FileUniqueID)
		if lerr != nil {
			return nil, fmt.Errorf("lookup duplicate: %w", lerr)
		}
		return AlreadySaved{Doc: other}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("replace content: %w", err)
	}
	if !replaced {
		return s.replaceRefused(ctx, user, id)
	}
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}
	return Replaced{Doc: doc}, nil
}

// replaceRefused explains why the guarded update touched no row.
func (s *Service) replaceRefused(ctx context.Context, user UserID, id DocumentID) (Reply, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return nil, fmt.Errorf("reload document: %w", err)
	}
	if doc.SyncStatus == SyncMissing {
		return ReplaceMissing{}, nil
	}
	// Uploading, or a sync in flight; if the state changed back between the two
	// statements, a retry by the user will succeed.
	return ReplaceBusy{}, nil
}
