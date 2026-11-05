package postgres

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// parseUUID turns a canonical UUID string into pgtype.UUID.
func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return u, nil
}
