package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// This file implements the docsbot.Repo port over the documents table,
// translating sqlc rows to and from domain types. The interface is declared by
// the consumer (the domain).

var _ docsbot.Repo = (*Repo)(nil)

// CreateDocument stores a received file as pending (category chosen later).
func (r *Repo) CreateDocument(ctx context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	uid, err := parseUUID(string(in.UserID))
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.InsertDocument(ctx, InsertDocumentParams{
		UserID:         uid,
		Status:         string(docsbot.StatusPending),
		Kind:           string(in.Kind),
		Category:       "",
		Title:          in.Title,
		TgFileID:       in.FileID,
		TgFileUniqueID: in.FileUniqueID,
	})
	if err != nil {
		return docsbot.Document{}, fmt.Errorf("insert document: %w", err)
	}
	return toDocument(row), nil
}

// DocumentByFileUniqueID returns the user's document for a Telegram file.
func (r *Repo) DocumentByFileUniqueID(ctx context.Context, user docsbot.UserID, uniqueID string) (docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.GetDocumentByUniqueID(ctx, GetDocumentByUniqueIDParams{UserID: uid, TgFileUniqueID: uniqueID})
	if err != nil {
		return docsbot.Document{}, notFoundOr(err, "get document by unique id")
	}
	return toDocument(row), nil
}

// DocumentByID returns the user's document by id.
func (r *Repo) DocumentByID(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.GetDocumentByID(ctx, GetDocumentByIDParams{ID: docID, UserID: uid})
	if err != nil {
		return docsbot.Document{}, notFoundOr(err, "get document by id")
	}
	return toDocument(row), nil
}

// SaveDocument sets the category and marks the document saved.
func (r *Repo) SaveDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.SaveDocumentCategory(ctx, SaveDocumentCategoryParams{ID: docID, UserID: uid, Category: string(cat)}); err != nil {
		return fmt.Errorf("save document category: %w", err)
	}
	return nil
}

// SetBackupStatus updates the Drive upload status.
func (r *Repo) SetBackupStatus(ctx context.Context, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	docID, err := parseUUID(string(id))
	if err != nil {
		return err
	}
	if err := r.UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{ID: docID, BackupStatus: string(s)}); err != nil {
		return fmt.Errorf("update backup status: %w", err)
	}
	return nil
}

// SetTitle renames a document.
func (r *Repo) SetTitle(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.UpdateDocumentTitle(ctx, UpdateDocumentTitleParams{ID: docID, UserID: uid, Title: title}); err != nil {
		return fmt.Errorf("update document title: %w", err)
	}
	return nil
}

// SetCategory moves a document to another category.
func (r *Repo) SetCategory(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.UpdateDocumentCategory(ctx, UpdateDocumentCategoryParams{ID: docID, UserID: uid, Category: string(cat)}); err != nil {
		return fmt.Errorf("update document category: %w", err)
	}
	return nil
}

// DeleteDocument removes a document.
func (r *Repo) DeleteDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.DeleteDocumentByID(ctx, DeleteDocumentByIDParams{ID: docID, UserID: uid}); err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// CountSaved counts the user's saved documents.
func (r *Repo) CountSaved(ctx context.Context, user docsbot.UserID) (int, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return 0, err
	}
	n, err := r.CountSavedDocuments(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("count saved documents: %w", err)
	}
	return int(n), nil
}

// CountNotBackedUp counts saved documents not yet on Drive.
func (r *Repo) CountNotBackedUp(ctx context.Context, user docsbot.UserID) (int, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return 0, err
	}
	n, err := r.CountDocumentsNotBackedUp(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("count documents not backed up: %w", err)
	}
	return int(n), nil
}

// CategoryCounts returns per-category saved-document counts.
func (r *Repo) CategoryCounts(ctx context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.DocumentCategoryCounts(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("category counts: %w", err)
	}
	out := make([]docsbot.CategoryCount, 0, len(rows))
	for _, row := range rows {
		out = append(out, docsbot.CategoryCount{Category: docsbot.Category(row.Category), Count: int(row.N)})
	}
	return out, nil
}

// ListByCategory returns one page of a category's saved documents, newest first.
func (r *Repo) ListByCategory(ctx context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ListDocumentsByCategory(ctx, ListDocumentsByCategoryParams{
		UserID:   uid,
		Category: string(cat),
		Lim:      int32(limit),
		Off:      int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list documents by category: %w", err)
	}
	return toDocuments(rows), nil
}

// Search finds saved documents matching query by title (ILIKE + trigram).
func (r *Repo) Search(ctx context.Context, user docsbot.UserID, query string, limit int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.SearchDocuments(ctx, SearchDocumentsParams{
		UserID: uid,
		Query:  pgtype.Text{String: query, Valid: true},
		Lim:    int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}
	return toDocuments(rows), nil
}

// ids parses a (document, user) id pair in one step.
func ids(id docsbot.DocumentID, user docsbot.UserID) (docID, uid pgtype.UUID, err error) {
	if docID, err = parseUUID(string(id)); err != nil {
		return
	}
	uid, err = parseUUID(string(user))
	return
}

// notFoundOr maps pgx.ErrNoRows to the domain ErrNotFound, else wraps with ctx.
func notFoundOr(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return docsbot.ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}

func toDocument(d Document) docsbot.Document {
	return docsbot.Document{
		ID:             docsbot.DocumentID(d.ID.String()),
		UserID:         docsbot.UserID(d.UserID.String()),
		Status:         docsbot.Status(d.Status),
		Category:       docsbot.Category(d.Category),
		Title:          d.Title,
		Kind:           docsbot.Kind(d.Kind),
		TgFileID:       d.TgFileID,
		TgFileUniqueID: d.TgFileUniqueID,
		DriveFileID:    d.DriveFileID.String,
		BackupStatus:   docsbot.BackupStatus(d.BackupStatus),
		CreatedAt:      d.CreatedAt.Time,
	}
}

func toDocuments(rows []Document) []docsbot.Document {
	out := make([]docsbot.Document, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDocument(d))
	}
	return out
}
