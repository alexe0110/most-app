package postgres

import (
	"context"

	"github.com/alexe0110/most-app/internal/docsbot"
)

var _ docsbot.Folders = (*Repo)(nil)

// CategoryFolderID returns the cached Drive folder id of the user's category.
func (r *Repo) CategoryFolderID(ctx context.Context, user docsbot.UserID, cat docsbot.Category) (string, bool, error) {
	return r.FolderID(ctx, string(user), string(cat))
}
