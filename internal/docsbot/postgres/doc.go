// Package postgres is the outbound adapter that persists docsbot entities. It
// combines sqlc-generated query code (*.sql.go — do not edit, run `make sqlc`)
// with hand-written adapters, one per port family (Documents, StatusMessages,
// Users, OAuthStore, FolderCache), that translate pgx errors into domain errors.
// Multi-write methods run in one transaction opened by inTx.
package postgres
