package docsbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// inlinePageSize is how many documents one inline answer carries.
const inlinePageSize = 20

// InlineState tells the channel adapter how to present an inline answer.
type InlineState int

// Inline answer states.
const (
	// InlineOK: the answer is the document list; Docs may be empty when nothing matched.
	InlineOK InlineState = iota
	// InlineUnknown: the sender never started the bot.
	InlineUnknown
	// InlineDisabled: the user turned inline mode off in /settings.
	InlineDisabled
	// InlineEmpty: the user has no saved documents at all.
	InlineEmpty
)

// InlineResult is the answer to an inline query. NextOffset is the offset of the
// next page, empty on the last one.
type InlineResult struct {
	State      InlineState
	Docs       []Document
	NextOffset string
}

// Inline answers an inline query: the user's own saved documents, the recent
// ones for a blank query, otherwise those matching by title or category label.
// An empty user is a sender who never started the bot; nothing is read for them.
func (s *Service) Inline(ctx context.Context, user UserID, query, offset string) (InlineResult, error) {
	if user == "" {
		return InlineResult{State: InlineUnknown}, nil
	}
	enabled, err := s.repo.InlineEnabled(ctx, user)
	if err != nil {
		return InlineResult{}, fmt.Errorf("inline enabled: %w", err)
	}
	if !enabled {
		return InlineResult{State: InlineDisabled}, nil
	}

	off := parseInlineOffset(offset)
	query = normalizeQuery(query)
	var docs []Document
	if query == "" {
		docs, err = s.repo.RecentSaved(ctx, user, inlinePageSize+1, off)
	} else {
		docs, err = s.repo.InlineSearch(ctx, user, query, categoriesMatching(query), inlinePageSize+1, off)
	}
	if err != nil {
		return InlineResult{}, fmt.Errorf("inline documents: %w", err)
	}

	res := InlineResult{State: InlineOK, Docs: docs}
	if len(docs) > inlinePageSize {
		res.Docs = docs[:inlinePageSize]
		res.NextOffset = strconv.Itoa(off + inlinePageSize)
	}
	if len(res.Docs) == 0 && off == 0 {
		total, err := s.repo.CountSaved(ctx, user)
		if err != nil {
			return InlineResult{}, fmt.Errorf("count saved: %w", err)
		}
		if total == 0 {
			res.State = InlineEmpty
		}
	}
	return res, nil
}

// SetInlineEnabled turns inline mode on or off for the user.
func (s *Service) SetInlineEnabled(ctx context.Context, user UserID, on bool) error {
	if err := s.repo.SetInlineEnabled(ctx, user, on); err != nil {
		return fmt.Errorf("set inline enabled: %w", err)
	}
	return nil
}

// parseInlineOffset reads Telegram's opaque offset: a non-negative number we
// issued, capped at MaxDocumentsPerUser; anything else starts from the top.
func parseInlineOffset(offset string) int {
	n, err := strconv.Atoi(offset)
	if err != nil || n < 0 {
		return 0
	}
	return min(n, MaxDocumentsPerUser)
}

// categoriesMatching lists the categories whose label contains query, ignoring case.
func categoriesMatching(query string) []Category {
	q := strings.ToLower(query)
	var out []Category
	for _, c := range AllCategories {
		if strings.Contains(strings.ToLower(c.Label()), q) {
			out = append(out, c)
		}
	}
	return out
}
