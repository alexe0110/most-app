package docsbottest

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func (r *FakeRepo) Search(_ context.Context, user docsbot.UserID, q string, limit int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q = strings.ToLower(q)
	var out []docsbot.Document
	for _, d := range r.SavedFor(user) {
		if strings.Contains(strings.ToLower(d.Title), q) {
			out = append(out, d)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// recentFor lists the user's saved documents, newest first (ties by id), like the SQL.
func (r *FakeRepo) recentFor(user docsbot.UserID) []docsbot.Document {
	out := r.SavedFor(user)
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// page applies limit/offset like the SQL.
func page(docs []docsbot.Document, limit, offset int) []docsbot.Document {
	if offset >= len(docs) {
		return nil
	}
	docs = docs[offset:]
	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs
}

func (r *FakeRepo) InlineSearch(_ context.Context, user docsbot.UserID, q string, cats []docsbot.Category, limit, offset int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q = strings.ToLower(q)
	var match []docsbot.Document
	for _, d := range r.recentFor(user) {
		if strings.Contains(strings.ToLower(d.Title), q) || slices.Contains(cats, d.Category) {
			match = append(match, d)
		}
	}
	return page(match, limit, offset), nil
}

func (r *FakeRepo) RecentSaved(_ context.Context, user docsbot.UserID, limit, offset int) ([]docsbot.Document, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return page(r.recentFor(user), limit, offset), nil
}

func (r *FakeRepo) InlineEnabled(_ context.Context, user docsbot.UserID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.InlineErr != nil {
		return false, r.InlineErr
	}
	return !r.InlineOff[user], nil
}

func (r *FakeRepo) SetInlineEnabled(_ context.Context, user docsbot.UserID, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.InlineOff == nil {
		r.InlineOff = map[docsbot.UserID]bool{}
	}
	r.InlineOff[user] = !on
	return nil
}
