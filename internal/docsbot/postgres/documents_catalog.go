package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/alexe0110/most-app/internal/docsbot"
)

// This file holds the read side of Documents: counts, listings and search.

// TitleExistsInCategory reports whether the user has a saved document with this
// title in the category.
func (r *Documents) TitleExistsInCategory(ctx context.Context, user docsbot.UserID, cat docsbot.Category, title string) (bool, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return false, err
	}
	ok, err := r.q.TitleExistsInCategory(ctx, TitleExistsInCategoryParams{UserID: uid, Category: string(cat), Title: title})
	if err != nil {
		return false, fmt.Errorf("title exists in category: %w", err)
	}
	return ok, nil
}

// CountSaved counts the user's saved documents.
func (r *Documents) CountSaved(ctx context.Context, user docsbot.UserID) (int, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return 0, err
	}
	n, err := r.q.CountSavedDocuments(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("count saved documents: %w", err)
	}
	return int(n), nil
}

// CountNotBackedUp counts saved documents not yet on Drive.
func (r *Documents) CountNotBackedUp(ctx context.Context, user docsbot.UserID) (int, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return 0, err
	}
	n, err := r.q.CountDocumentsNotBackedUp(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("count documents not backed up: %w", err)
	}
	return int(n), nil
}

// CategoryCounts returns per-category saved-document counts.
func (r *Documents) CategoryCounts(ctx context.Context, user docsbot.UserID) ([]docsbot.CategoryCount, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.DocumentCategoryCounts(ctx, uid)
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
func (r *Documents) ListByCategory(ctx context.Context, user docsbot.UserID, cat docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.ListDocumentsByCategory(ctx, ListDocumentsByCategoryParams{
		UserID:   uid,
		Category: string(cat),
		Known:    knownCategories(),
		Lim:      int32(limit),
		Off:      int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("list documents by category: %w", err)
	}
	return toDocuments(rows), nil
}

// knownCategories are the current category slugs; "other" also collects the rows
// whose slug is not among them.
func knownCategories() []string {
	out := make([]string, len(docsbot.AllCategories))
	for i, c := range docsbot.AllCategories {
		out[i] = string(c)
	}
	return out
}

// likeEscaper escapes the LIKE metacharacters so user input matches literally;
// the query's ILIKE uses the default backslash escape.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Search finds saved documents matching query by title (ILIKE + trigram).
func (r *Documents) Search(ctx context.Context, user docsbot.UserID, query string, limit int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.SearchDocuments(ctx, SearchDocumentsParams{
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

// InlineSearch finds saved documents whose title matches query or whose category
// is in cats, best match first.
func (r *Documents) InlineSearch(ctx context.Context, user docsbot.UserID, query string, cats []docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	slugs := make([]string, len(cats))
	for i, c := range cats {
		slugs[i] = string(c)
	}
	rows, err := r.q.InlineSearchDocuments(ctx, InlineSearchDocumentsParams{
		UserID:  uid,
		Pattern: pgtype.Text{String: likeEscaper.Replace(query), Valid: true},
		Query:   query,
		Cats:    slugs,
		Lim:     int32(limit),
		Off:     int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("inline search documents: %w", err)
	}
	return toDocuments(rows), nil
}

// RecentSaved lists the user's saved documents, newest first.
func (r *Documents) RecentSaved(ctx context.Context, user docsbot.UserID, limit, offset int) ([]docsbot.Document, error) {
	uid, err := parseUUID(string(user))
	if err != nil {
		return nil, err
	}
	rows, err := r.q.RecentSavedDocuments(ctx, RecentSavedDocumentsParams{UserID: uid, Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, fmt.Errorf("recent saved documents: %w", err)
	}
	return toDocuments(rows), nil
}
