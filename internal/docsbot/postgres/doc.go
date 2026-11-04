// Package postgres is the outbound adapter that persists docsbot entities.
//
// It combines sqlc-generated query code (db.go, models.go, *.sql.go — do not
// edit by hand, run `make sqlc`) with a hand-written Repo that adds a
// transaction helper. In #10 this is where pgx errors get translated into
// domain errors (pgx.ErrNoRows -> docsbot.ErrNotFound).
package postgres
