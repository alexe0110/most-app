package docsbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// pageSize is how many documents one catalog page shows (#13).
	pageSize = 10
	// searchLimit caps search results (#14).
	searchLimit = 10
)

// Service holds the Most Docs use cases. It depends only on ports, never on
// Telegram, Postgres or Drive SDKs.
type Service struct {
	repo  Repo
	drive Drive
}

// NewService wires the service around its ports.
func NewService(repo Repo, drive Drive) *Service {
	return &Service{repo: repo, drive: drive}
}

// Receive stores a freshly sent file as pending and asks for a category. A file
// already saved returns AlreadySaved; a still-pending one re-asks the category.
func (s *Service) Receive(ctx context.Context, in IncomingFile) (Reply, error) {
	existing, err := s.repo.DocumentByFileUniqueID(ctx, in.UserID, in.FileUniqueID)
	switch {
	case err == nil && existing.Status == StatusSaved:
		return AlreadySaved{Doc: existing}, nil
	case err == nil:
		return NeedCategory{Doc: existing}, nil
	case !errors.Is(err, ErrNotFound):
		return nil, fmt.Errorf("lookup document: %w", err)
	}

	doc, err := s.repo.CreateDocument(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}
	return NeedCategory{Doc: doc}, nil
}

// ChooseCategory saves a pending document under cat. It is idempotent: a second
// tap on an already-saved document just returns it. When Drive is connected the
// document is queued for upload (#11 performs it); otherwise it stays in
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
		return Saved{Doc: doc}, nil
	}

	if err := s.repo.SaveDocument(ctx, user, id, cat); err != nil {
		return Saved{}, fmt.Errorf("save document: %w", err)
	}
	doc.Category = cat
	doc.Status = StatusSaved

	connected, err := s.drive.Connected(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("drive status: %w", err)
	}
	if connected {
		// TODO(#11): enqueue upload_document in the same tx as this status.
		if err := s.repo.SetBackupStatus(ctx, id, BackupPending); err != nil {
			return Saved{}, fmt.Errorf("mark backup pending: %w", err)
		}
		doc.BackupStatus = BackupPending
		return Saved{Doc: doc}, nil
	}

	count, err := s.repo.CountSaved(ctx, user)
	if err != nil {
		return Saved{}, fmt.Errorf("count saved: %w", err)
	}
	return Saved{Doc: doc, OfferDrive: count == 1 || count == 5}, nil
}

// Card returns one document's detail, scoped to the user.
func (s *Service) Card(ctx context.Context, user UserID, id DocumentID) (Card, error) {
	doc, err := s.repo.DocumentByID(ctx, user, id)
	if err != nil {
		return Card{}, fmt.Errorf("load document: %w", err)
	}
	return Card{Doc: doc}, nil
}

// Rename changes a document's title to a non-blank value.
func (s *Service) Rename(ctx context.Context, user UserID, id DocumentID, title string) (Card, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return Card{}, ErrEmptyTitle
	}
	if err := s.repo.SetTitle(ctx, user, id, title); err != nil {
		return Card{}, fmt.Errorf("rename document: %w", err)
	}
	return s.Card(ctx, user, id)
}

// Recategorize moves a document to another category.
func (s *Service) Recategorize(ctx context.Context, user UserID, id DocumentID, cat Category) (Card, error) {
	if !cat.Valid() {
		return Card{}, ErrUnknownCategory
	}
	if err := s.repo.SetCategory(ctx, user, id, cat); err != nil {
		return Card{}, fmt.Errorf("recategorize document: %w", err)
	}
	// TODO(#11): if backed up, move the file to the new Drive folder.
	return s.Card(ctx, user, id)
}

// Delete removes a document, scoped to the user.
func (s *Service) Delete(ctx context.Context, user UserID, id DocumentID) error {
	// TODO(#11): if backed up, delete the file on Drive too.
	if err := s.repo.DeleteDocument(ctx, user, id); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
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
	return Catalog{Counts: counts, NotInDrive: notInDrive, DriveConnected: connected}, nil
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
	return CategoryPage{
		Category: cat, Docs: docs, Page: page, Pages: pages, Total: total, DriveConnected: connected,
	}, nil
}

// Search finds up to searchLimit saved documents matching query.
func (s *Service) Search(ctx context.Context, user UserID, query string) (Found, error) {
	query = strings.TrimSpace(query)
	docs, err := s.repo.Search(ctx, user, query, searchLimit)
	if err != nil {
		return Found{}, fmt.Errorf("search documents: %w", err)
	}
	return Found{Query: query, Docs: docs}, nil
}

// categoryTotal reads the saved-document count for one category.
func (s *Service) categoryTotal(ctx context.Context, user UserID, cat Category) (int, error) {
	counts, err := s.repo.CategoryCounts(ctx, user)
	if err != nil {
		return 0, fmt.Errorf("category counts: %w", err)
	}
	for _, c := range counts {
		if c.Category == cat {
			return c.Count, nil
		}
	}
	return 0, nil
}

func clamp(v, lo, hi int) int {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}
