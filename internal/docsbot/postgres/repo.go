package postgres

import "github.com/jackc/pgx/v5/pgxpool"

// Repo persists docsbot entities in Postgres. It embeds the sqlc-generated
// *Queries, so every query is available on Repo.
type Repo struct {
	pool *pgxpool.Pool
	jobs JobInserter
	*Queries
}

// NewRepo wires a Repo around an existing pool; jobs enqueues the jobs that
// commit together with a document change.
func NewRepo(pool *pgxpool.Pool, jobs JobInserter) *Repo {
	return &Repo{pool: pool, jobs: jobs, Queries: New(pool)}
}
