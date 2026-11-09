package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
		FileSize:       pgtype.Int8{Int64: in.FileSize, Valid: in.FileSize > 0},
		MimeType:       pgtype.Text{String: in.MIME, Valid: in.MIME != ""},
		MediaGroupID:   pgtype.Text{String: in.MediaGroupID, Valid: in.MediaGroupID != ""},
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

// SaveDocument sets the category and marks a pending document saved. saved is
// false when the document was not pending (already saved, or gone).
func (r *Repo) SaveDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) (saved bool, err error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	n, err := r.SaveDocumentCategory(ctx, SaveDocumentCategoryParams{ID: docID, UserID: uid, Category: string(cat)})
	if err != nil {
		return false, fmt.Errorf("save document category: %w", err)
	}
	return n == 1, nil
}

// SetBackupStatus updates the Drive upload status.
func (r *Repo) SetBackupStatus(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, s docsbot.BackupStatus) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.UpdateDocumentBackupStatus(ctx, UpdateDocumentBackupStatusParams{ID: docID, UserID: uid, BackupStatus: string(s)}); err != nil {
		return fmt.Errorf("update backup status: %w", err)
	}
	return nil
}

// ClaimFailed atomically moves a failed document to pending; false when it is
// not failed or not the user's.
func (r *Repo) ClaimFailed(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (bool, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return false, err
	}
	n, err := r.ClaimFailedForRetry(ctx, ClaimFailedForRetryParams{ID: docID, UserID: uid})
	if err != nil {
		return false, fmt.Errorf("claim failed for retry: %w", err)
	}
	return n > 0, nil
}

// MarkUploading sets the status to uploading without touching updated_at, so
// the sweeper's 24h backstop counts from enqueue time.
func (r *Repo) MarkUploading(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	if err := r.MarkDocumentUploading(ctx, MarkDocumentUploadingParams{ID: docID, UserID: uid}); err != nil {
		return fmt.Errorf("mark document uploading: %w", err)
	}
	return nil
}

// StuckUploads lists pending/uploading saved documents last touched before
// olderThan, for users with an active Drive integration.
func (r *Repo) StuckUploads(ctx context.Context, olderThan time.Time, limit int) ([]docsbot.StuckUpload, error) {
	rows, err := r.Queries.StuckUploads(ctx, StuckUploadsParams{
		OlderThan: pgtype.Timestamptz{Time: olderThan, Valid: true},
		Lim:       int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("stuck uploads: %w", err)
	}
	out := make([]docsbot.StuckUpload, 0, len(rows))
	for _, row := range rows {
		out = append(out, docsbot.StuckUpload{
			User:      docsbot.UserID(row.UserID.String()),
			ID:        docsbot.DocumentID(row.ID.String()),
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return out, nil
}

// ResetPendingUploads resets the user's pending/uploading saved documents to none.
func (r *Repo) ResetPendingUploads(ctx context.Context, user docsbot.UserID) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	if err := r.Queries.ResetPendingUploads(ctx, uid); err != nil {
		return fmt.Errorf("reset pending uploads: %w", err)
	}
	return nil
}

// SetTitle renames a document.
func (r *Repo) SetTitle(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, title string) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	n, err := r.UpdateDocumentTitle(ctx, UpdateDocumentTitleParams{ID: docID, UserID: uid, Title: title})
	if err != nil {
		return fmt.Errorf("update document title: %w", err)
	}
	if n == 0 {
		return docsbot.ErrNotFound
	}
	return nil
}

// SetCategory moves a document to another category.
func (r *Repo) SetCategory(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, cat docsbot.Category) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	n, err := r.UpdateDocumentCategory(ctx, UpdateDocumentCategoryParams{ID: docID, UserID: uid, Category: string(cat)})
	if err != nil {
		return fmt.Errorf("update document category: %w", err)
	}
	if n == 0 {
		return docsbot.ErrNotFound
	}
	return nil
}

// TitleExistsInCategory reports whether the user has a saved document with this
// title in the category.
func (r *Repo) TitleExistsInCategory(ctx context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return false, err
	}
	ok, err := r.Queries.TitleExistsInCategory(ctx, TitleExistsInCategoryParams{UserID: uid, Category: string(cat), Title: title})
	if err != nil {
		return false, fmt.Errorf("title exists in category: %w", err)
	}
	return ok, nil
}

// DeleteDocument removes a document.
func (r *Repo) DeleteDocument(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	n, err := r.DeleteDocumentByID(ctx, DeleteDocumentByIDParams{ID: docID, UserID: uid})
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	if n == 0 {
		return docsbot.ErrNotFound
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

// likeEscaper escapes the LIKE metacharacters so user input matches literally;
// the query's ILIKE uses the default backslash escape.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Search finds saved documents matching query by title (ILIKE + trigram).
func (r *Repo) Search(ctx context.Context, user docsbot.UserID, query string, limit int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.SearchDocuments(ctx, SearchDocumentsParams{
		UserID:  uid,
		Pattern: pgtype.Text{String: likeEscaper.Replace(query), Valid: true},
		Query:   query,
		Lim:     int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("search documents: %w", err)
	}
	return toDocuments(rows), nil
}

// MarkBackedUp records a successful Drive upload.
func (r *Repo) MarkBackedUp(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID, driveFileID string) error {
	docID, uid, err := ids(id, user)
	if err != nil {
		return err
	}
	err = r.MarkDocumentBackedUp(ctx, MarkDocumentBackedUpParams{
		ID:          docID,
		UserID:      uid,
		DriveFileID: pgtype.Text{String: driveFileID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("mark document backed up: %w", err)
	}
	return nil
}

// ClaimTransferable atomically moves every saved none/failed document to
// pending and returns their ids, oldest first.
func (r *Repo) ClaimTransferable(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ClaimTransferableDocuments(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("claim transferable documents: %w", err)
	}
	ids := make([]claimedID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, claimedID{id: row.ID, createdAt: row.CreatedAt.Time})
	}
	return sortedIDs(ids), nil
}

// ClaimAllFailed atomically moves every failed upload to pending and returns
// their ids, oldest first.
func (r *Repo) ClaimAllFailed(ctx context.Context, user docsbot.UserID) ([]docsbot.DocumentID, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ClaimFailedDocuments(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("claim failed documents: %w", err)
	}
	ids := make([]claimedID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, claimedID{id: row.ID, createdAt: row.CreatedAt.Time})
	}
	return sortedIDs(ids), nil
}

// claimedID is a claimed document with the time to order the batch by.
type claimedID struct {
	id        pgtype.UUID
	createdAt time.Time
}

// sortedIDs orders claimed documents oldest first (UPDATE ... RETURNING has no order).
func sortedIDs(in []claimedID) []docsbot.DocumentID {
	slices.SortFunc(in, func(a, b claimedID) int { return a.createdAt.Compare(b.createdAt) })
	out := make([]docsbot.DocumentID, 0, len(in))
	for _, c := range in {
		out = append(out, docsbot.DocumentID(c.id.String()))
	}
	return out
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
	var statusID docsbot.StatusMessageID
	if d.StatusMessageID.Valid {
		statusID = docsbot.StatusMessageID(d.StatusMessageID.String())
	}
	return docsbot.Document{
		StatusMessageID: statusID,
		ID:              docsbot.DocumentID(d.ID.String()),
		UserID:          docsbot.UserID(d.UserID.String()),
		Status:          docsbot.Status(d.Status),
		Category:        docsbot.Category(d.Category),
		Title:           d.Title,
		Kind:            docsbot.Kind(d.Kind),
		TgFileID:        d.TgFileID,
		TgFileUniqueID:  d.TgFileUniqueID,
		DriveFileID:     d.DriveFileID.String,
		FileSize:        d.FileSize.Int64,
		MIME:            d.MimeType.String,
		MediaGroupID:    d.MediaGroupID.String,
		BackupStatus:    docsbot.BackupStatus(d.BackupStatus),
		CreatedAt:       d.CreatedAt.Time,
	}
}

func toDocuments(rows []Document) []docsbot.Document {
	out := make([]docsbot.Document, 0, len(rows))
	for _, d := range rows {
		out = append(out, toDocument(d))
	}
	return out
}

// PendingByMediaGroup lists the user's pending documents of one album, oldest first.
func (r *Repo) PendingByMediaGroup(ctx context.Context, user docsbot.UserID, group string) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.ListPendingByMediaGroup(ctx, ListPendingByMediaGroupParams{
		UserID:       uid,
		MediaGroupID: pgtype.Text{String: group, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list pending by media group: %w", err)
	}
	return toDocuments(rows), nil
}

// IntakeStats returns the user's pending count, files created since `since`
// and total documents in one query.
func (r *Repo) IntakeStats(ctx context.Context, user docsbot.UserID, since time.Time) (docsbot.IntakeStats, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.IntakeStats{}, err
	}
	row, err := r.UserIntakeStats(ctx, UserIntakeStatsParams{
		UserID: uid,
		Since:  pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		return docsbot.IntakeStats{}, fmt.Errorf("user intake stats: %w", err)
	}
	return docsbot.IntakeStats{Pending: int(row.Pending), Recent: int(row.Recent), Total: int(row.Total)}, nil
}

// PurgeStalePending deletes pending documents created before olderThan, for all
// users, and returns how many were removed.
func (r *Repo) PurgeStalePending(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := r.DeleteStalePending(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete stale pending: %w", err)
	}
	return int(n), nil
}
