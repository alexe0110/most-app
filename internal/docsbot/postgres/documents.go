package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// Documents persists documents in Postgres. It implements the docsbot ports Repo,
// Syncs and Uploads, translating sqlc rows to and from domain types; Syncs and
// Uploads also queue their jobs through JobInserter inside the same transaction.
type Documents struct {
	pool *pgxpool.Pool
	q    *Queries
	jobs JobInserter
}

var (
	_ docsbot.Repo    = (*Documents)(nil)
	_ docsbot.Syncs   = (*Documents)(nil)
	_ docsbot.Uploads = (*Documents)(nil)
)

// NewDocuments wires Documents around an existing pool; jobs enqueues the jobs
// that commit together with a document change.
func NewDocuments(pool *pgxpool.Pool, jobs JobInserter) *Documents {
	return &Documents{pool: pool, q: New(pool), jobs: jobs}
}

// CreateDocument stores a received file as pending (category chosen later).
func (r *Documents) CreateDocument(ctx context.Context, in docsbot.IncomingFile) (docsbot.Document, error) {
	uid, err := parseUUID(string(in.UserID))
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.q.InsertDocument(ctx, InsertDocumentParams{
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
		PageCount:      int32(in.PageCount),
		ImageWidth:     int32(in.Width),
		ImageHeight:    int32(in.Height),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return docsbot.Document{}, docsbot.ErrDuplicateFile // on conflict do nothing returned no row
	}
	if err != nil {
		return docsbot.Document{}, fmt.Errorf("insert document: %w", err)
	}
	return toDocument(row), nil
}

// DocumentByFileUniqueID returns the user's document for a Telegram file.
func (r *Documents) DocumentByFileUniqueID(ctx context.Context, user docsbot.UserID, uniqueID string) (docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.q.GetDocumentByUniqueID(ctx, GetDocumentByUniqueIDParams{UserID: uid, TgFileUniqueID: uniqueID})
	if err != nil {
		return docsbot.Document{}, notFoundOr(err, "get document by unique id")
	}
	return toDocument(row), nil
}

// DocumentByID returns the user's document by id.
func (r *Documents) DocumentByID(ctx context.Context, user docsbot.UserID, id docsbot.DocumentID) (docsbot.Document, error) {
	docID, uid, err := ids(id, user)
	if err != nil {
		return docsbot.Document{}, err
	}
	row, err := r.q.GetDocumentByID(ctx, GetDocumentByIDParams{ID: docID, UserID: uid})
	if err != nil {
		return docsbot.Document{}, notFoundOr(err, "get document by id")
	}
	return toDocument(row), nil
}

// PendingByMediaGroup lists the user's pending documents of one album, oldest first.
func (r *Documents) PendingByMediaGroup(ctx context.Context, user docsbot.UserID, group string) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListPendingByMediaGroup(ctx, ListPendingByMediaGroupParams{
		UserID:       uid,
		MediaGroupID: pgtype.Text{String: group, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("list pending by media group: %w", err)
	}
	return toDocuments(rows), nil
}

// NumberPendingAlbum retitles the user's pending documents of one album "base (n)" in arrival order.
func (r *Documents) NumberPendingAlbum(ctx context.Context, user docsbot.UserID, group, base string) error {
	uid, err := parseUUID(string(user))
	if err != nil {
		return err
	}
	err = r.q.NumberPendingAlbum(ctx, NumberPendingAlbumParams{
		UserID:       uid,
		MediaGroupID: pgtype.Text{String: group, Valid: true},
		Base:         base,
	})
	if err != nil {
		return fmt.Errorf("number pending album: %w", err)
	}
	return nil
}

// IntakeStats returns the user's pending count, files created since `since`
// and total documents in one query.
func (r *Documents) IntakeStats(ctx context.Context, user docsbot.UserID, since time.Time) (docsbot.IntakeStats, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return docsbot.IntakeStats{}, err
	}
	row, err := r.q.UserIntakeStats(ctx, UserIntakeStatsParams{
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
func (r *Documents) PurgeStalePending(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := r.q.DeleteStalePending(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("delete stale pending: %w", err)
	}
	return int(n), nil
}

// ids parses a (document, user) id pair in one step.
func ids(id docsbot.DocumentID, user docsbot.UserID) (docID, uid pgtype.UUID, err error) {
	if docID, err = parseUUID(string(id)); err != nil {
		return
	}
	uid, err = parseUUID(string(user))
	return
}

// uniqueViolation is the SQLSTATE of a unique constraint violation.
const uniqueViolation = "23505"

// isUniqueViolation reports whether err is a unique constraint violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
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
		PageCount:       int(d.PageCount.Int32),
		Width:           int(d.ImageWidth.Int32),
		Height:          int(d.ImageHeight.Int32),
		MIME:            d.MimeType.String,
		MediaGroupID:    d.MediaGroupID.String,
		BackupStatus:    docsbot.BackupStatus(d.BackupStatus),
		SyncStatus:      docsbot.SyncStatus(d.SyncStatus),
		SyncReason:      d.SyncReason.String,
		BackupReason:    docsbot.BackupReason(d.BackupReason.String),
		MetaVersion:     int(d.MetaVersion),
		ContentDirty:    d.ContentDirty,
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
