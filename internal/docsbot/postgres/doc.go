// Package postgres is the outbound adapter that persists docsbot entities. It
// combines sqlc-generated query code (*.sql.go — do not edit, run `make sqlc`)
// with a hand-written Repo that adds a transaction helper and translates pgx
// errors into domain errors.
package postgres
