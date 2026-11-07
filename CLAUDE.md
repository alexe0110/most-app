# CLAUDE.md — Most

Most is a series of Telegram bots built on a shared Go core. The first bot, Most Docs, saves documents to the user's own Google Drive; until Drive is connected, documents are kept in Telegram.

Docs (read only what the task needs, not everything): `docs/code_conventions.md` (read before writing code; skim the relevant sections), `docs/product.md`, `docs/architecture.md`, `docs/model.md`. Tasks: GitHub Issues (milestone MVP).

## Architecture

- `internal/<bot>/` — domain: entities, `Service` with use cases, ports in `ports.go`.
- `internal/<bot>/<adapter>/` — adapters: `telegram/`, `jobs/` (inbound), `postgres/` (outbound).
- `internal/platform/` — shared core for all bots (config, log, db, jobs, tg, oauth, gdrive, httpx, billing).
- `cmd/<bot>/main.go` — manual dependency wiring, no DI frameworks.
- Use cases return the meaning of a reply (`docsbot.Reply`); the channel adapter renders buttons and texts.

## Hard rules

0. Idiomatic, clear, tidy Go.
1. The domain (`internal/docsbot`) must not import adapters, `platform/tg`, `platform/db`, `platform/gdrive` or external SDKs (depguard).
2. Bots never import each other. `platform` never imports a bot.
3. Interfaces are declared by the consumer; add one only for a second implementation or a test fake.
4. `ctx context.Context` is the first argument of every function that does I/O. Never store it in a struct.
5. Wrap errors: `fmt.Errorf("...: %w", err)`. Adapters translate their errors into domain errors. Log once, at the top (handler or worker).
6. SQL only in `.sql` files for `sqlc`, inside `postgres/`. Migrations: `goose`, in `migrations/`.
7. Never log tokens, document contents, file captions, IIN or document numbers.
8. No `utils`, `common`, `helpers` or `models` packages.
9. No new dependencies in `go.mod` unless explicitly asked.

## Adding a feature

1. Domain: entities / errors / `Reply` types, if needed.
2. `Service` method + table-driven unit test (fakes in `fakes_test.go`).
3. Port in `ports.go`, if a new external action is needed.
4. Port implementation in an adapter + integration test (`//go:build integration`).
5. Render the `Reply` in `telegram/`.
6. Wire in `cmd/<bot>/main.go`, if a new dependency appeared.

Reference: `docsbot.Service.Receive` and `internal/docsbot/telegram/docs.go`. Follow the same shape.

## Commands

```bash
make test         # unit tests
make test-int     # integration tests (needs Docker)
make lint         # golangci-lint + depguard
make migrate      # goose up ($DATABASE_URL)
make sqlc         # regenerate queries
make run          # local run, long polling, dev bot
```

Before "done": `make lint && make test` pass. Add `make test-int` if an adapter or SQL changed.

## Working style

- One task = one PR (`Closes #N`).
- Stay inside the task. Spotted a problem elsewhere — mention it in the PR, don't fix it.
- Unsure where code belongs — ask; don't invent a layer.
- Read only the files you need; use Grep/Glob instead of reading whole directories.
- Keep command output short: `go test ./path/...` for the touched package first, then `make test`.
- Code comments in English. User-facing texts in Russian, in `internal/<bot>/telegram/texts.go`.