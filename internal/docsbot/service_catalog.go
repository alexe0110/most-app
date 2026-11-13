package docsbot

import (
	"context"
	"fmt"
	"strings"
)

// Card returns one document's detail, scoped to the user.
func (s *Service) Card(ctx context.Context, user UserID, id DocumentID) (Card, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Card{}, fmt.Errorf("load document: %w", err)
	}
	if doc.InDrive() {
		// The file may still sit in an older category folder until the sync worker
		// moves it, so link only to the file itself.
		return Card{Doc: doc}, nil
	}
	folderID, _, err := s.folders.CategoryFolderID(ctx, user, doc.Category)
	if err != nil {
		return Card{}, fmt.Errorf("category folder: %w", err)
	}
	return Card{Doc: doc, CategoryFolderID: folderID}, nil
}

// OpenCard is Card for a card the user opens from a list or search: when the
// document has a live Drive file it also checks, at most once a minute and within
// remoteCheckTimeout, that the file still exists, and flips the sync status
// ok <-> missing accordingly. A failure of the check itself (network, timeout,
// revoked or disconnected Drive) is not an error: the card stays as it is. A
// database error comes back together with the card read so far (Card.Doc.ID set),
// which the caller may still show; nothing is re-uploaded here.
func (s *Service) OpenCard(ctx context.Context, user UserID, id DocumentID) (Card, error) {
	card, err := s.Card(ctx, user, id)
	if err != nil {
		return Card{}, err
	}
	doc := card.Doc
	if !doc.InDrive() || doc.DriveFileID == "" || (doc.SyncStatus != SyncOK && doc.SyncStatus != SyncMissing) {
		return card, nil
	}
	if !s.allowRemoteCheck(doc.ID) {
		return card, nil
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return card, fmt.Errorf("drive status: %w", err)
	}
	if !connected {
		return card, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, remoteCheckTimeout)
	defer cancel()
	exists, err := s.drive.FileExists(checkCtx, user, doc.DriveFileID)
	if err != nil {
		return card, nil
	}

	from, to := SyncOK, SyncMissing
	if exists {
		from, to = SyncMissing, SyncOK
	}
	if doc.SyncStatus != from {
		return card, nil
	}
	moved, err := s.repo.TransitionSyncStatus(ctx, user, doc.ID, doc.DriveFileID, from, to)
	if err != nil {
		return card, fmt.Errorf("transition sync status: %w", err)
	}
	if !moved {
		return card, nil
	}
	card.Doc.SyncStatus, card.Doc.SyncReason = to, ""
	return card, nil
}

// allowRemoteCheck reports whether the document's Drive file may be checked now,
// and records the attempt. Entries older than the throttle window are dropped on
// the way, so the map does not grow without bound.
func (s *Service) allowRemoteCheck(id DocumentID) bool {
	now := s.now()
	s.checksMu.Lock()
	defer s.checksMu.Unlock()
	for k, t := range s.checks {
		if now.Sub(t) >= remoteCheckEvery {
			delete(s.checks, k)
		}
	}
	if _, recent := s.checks[id]; recent {
		return false
	}
	s.checks[id] = now
	return true
}

// Rename changes a document's title to a normalized, non-blank value of at most
// MaxTitleRunes runes: a longer title is refused (ErrTitleTooLong), not cut. When
// Drive is connected a backed-up document's file is renamed by the sync worker.
func (s *Service) Rename(ctx context.Context, user UserID, id DocumentID, title string) (Card, error) {
	title = collapseTitle(title)
	if title == "" {
		return Card{}, ErrEmptyTitle
	}
	if len([]rune(title)) > MaxTitleRunes {
		return Card{}, ErrTitleTooLong
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Card{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		_, err = s.syncs.SetTitle(ctx, user, id, title)
	} else {
		err = s.repo.SetTitle(ctx, user, id, title)
	}
	if err != nil {
		return Card{}, fmt.Errorf("rename document: %w", err)
	}
	return s.Card(ctx, user, id)
}

// Recategorize moves a document to another category. When Drive is connected a
// backed-up document's file is moved by the sync worker; otherwise it stays in
// its old Drive folder and the card says so.
func (s *Service) Recategorize(ctx context.Context, user UserID, id DocumentID, cat Category) (Card, error) {
	if !cat.Valid() {
		return Card{}, ErrUnknownCategory
	}
	old, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Card{}, fmt.Errorf("load document: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Card{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		_, err = s.syncs.SetCategory(ctx, user, id, cat)
	} else {
		err = s.repo.SetCategory(ctx, user, id, cat)
	}
	if err != nil {
		return Card{}, fmt.Errorf("recategorize document: %w", err)
	}
	card, err := s.Card(ctx, user, id)
	if err != nil {
		return Card{}, err
	}
	// Without a connected cloud nothing moves the file: it stays in its old folder.
	// The card links to the file itself, and the new category's folder would mislead.
	if !connected && card.Doc.InDrive() && old.Category != cat {
		card.FileInOldFolder = true
		card.CategoryFolderID = ""
	}
	return card, nil
}

// Delete removes a document, scoped to the user. A backed-up document's cloud file
// is moved to the trash by a job queued with the row deletion; when Drive is not
// connected the file stays and the result says so.
func (s *Service) Delete(ctx context.Context, user UserID, id DocumentID) (Deleted, error) {
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Deleted{}, fmt.Errorf("drive status: %w", err)
	}
	// Whether the Drive file is trashed is decided on the deleted row itself.
	kept, err := s.syncs.DeleteDocument(ctx, user, id, connected)
	if err != nil {
		return Deleted{}, fmt.Errorf("delete document: %w", err)
	}
	return Deleted{RemoteKept: kept}, nil
}

// Catalog lists the user's categories with counts. The 📥 banner (NotInDrive)
// is only meaningful once Drive is connected.
func (s *Service) Catalog(ctx context.Context, user UserID) (Catalog, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return Catalog{}, fmt.Errorf("category counts: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Catalog{}, fmt.Errorf("drive status: %w", err)
	}
	var notInDrive int
	if connected {
		if notInDrive, err = s.repo.CountNotBackedUp(ctx, user); err != nil {
			return Catalog{}, fmt.Errorf("count not backed up: %w", err)
		}
	}
	return Catalog{Counts: foldCategoryCounts(counts), NotInDrive: notInDrive, DriveConnected: connected}, nil
}

// CategoryPage returns one page (pageSize) of a category, newest first. page is
// 1-based and clamped into range.
func (s *Service) CategoryPage(ctx context.Context, user UserID, cat Category, page int) (CategoryPage, error) {
	if !cat.Valid() {
		return CategoryPage{}, ErrUnknownCategory
	}
	total, err := s.categoryTotal(ctx, user, cat)
	if err != nil {
		return CategoryPage{}, err
	}
	pages := (total + pageSize - 1) / pageSize
	if pages == 0 {
		pages = 1
	}
	page = clamp(page, 1, pages)

	docs, err := s.repo.ListByCategory(ctx, user, cat, pageSize, (page-1)*pageSize)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("list by category: %w", err)
	}
	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("drive status: %w", err)
	}
	folderID, _, err := s.folders.CategoryFolderID(ctx, user, cat)
	if err != nil {
		return CategoryPage{}, fmt.Errorf("category folder: %w", err)
	}
	return CategoryPage{
		Category: cat, Docs: docs, Page: page, Pages: pages, Total: total, DriveConnected: connected,
		FolderID: folderID,
	}, nil
}

// Search finds up to searchLimit saved documents matching query. The query is
// trimmed and cut to maxQueryRunes; a blank one is not searched (BlankQuery).
func (s *Service) Search(ctx context.Context, user UserID, query string) (Found, error) {
	query = normalizeQuery(query)
	if query == "" {
		return Found{BlankQuery: true}, nil
	}
	docs, err := s.repo.Search(ctx, user, query, searchLimit)
	if err != nil {
		return Found{}, fmt.Errorf("search documents: %w", err)
	}
	return Found{Query: query, Docs: docs}, nil
}

// normalizeQuery trims a search query and cuts it to maxQueryRunes.
func normalizeQuery(query string) string {
	query = strings.TrimSpace(query)
	if r := []rune(query); len(r) > maxQueryRunes {
		query = strings.TrimSpace(string(r[:maxQueryRunes]))
	}
	return query
}

// categoryTotal reads the saved-document count for one category.
func (s *Service) categoryTotal(ctx context.Context, user UserID, cat Category) (int, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("category counts: %w", err)
	}
	for _, c := range foldCategoryCounts(counts) {
		if c.Category == cat {
			return c.Count, nil
		}
	}
	return 0, nil
}
