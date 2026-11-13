# CLAUDE.md — Most

Most is a series of Telegram bots built on a shared Go core. The first bot, Most Files (`docsbot`), saves documents to the user's own Google Drive; until Drive is connected, documents are kept in Telegram.

Docs (read only what the task needs, not everything): `docs/code_conventions.md` (read before writing code; skim the relevant sections), `docs/product.md`, `docs/architecture.md`, `docs/model.md`. Tasks: GitHub Issues (milestone MVP).

## Architecture

- `internal/<bot>/` — domain: entities, `Service` with use cases, ports in `ports.go`.
- `internal/<bot>/<adapter>/` — adapters: `telegram/`, `jobs/` (inbound), `postgres/` (outbound).
- `internal/platform/` — shared core for all bots (config, logging, db, jobs, tg, oauth, gdrive, convert).
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

## Code rules

1. Comments explain why, not what. No ticket numbers or change history in code, names or tests (only `TODO(#N)`); that lives in the commit and PR.
2. Grep for an existing helper before writing a new one; reuse it.
3. Keep files under ~400 lines, functions under ~60, at most 5 parameters (more: a struct). Split by meaning, don't append.
4. Tests sit next to the code and are named by behavior: `foo.go` -> `foo_test.go`. No new `*_qa_test.go`, `*_edge_test.go`, `review*_test.go` (existing ones get folded in); a new case is a row in an existing table.
5. Several writes (DB twice, or DB and queue) are one transaction, one port method. Before the PR answer: what remains if the process dies between the steps?
6. In-memory state needs a TTL, a size cap and cleanup, never file bytes; losing it on restart must be harmless and covered in `restart_test.go`.
7. Every external call has a timeout; `http.DefaultClient` is forbidden; heavy CPU or memory work goes under a semaphore.
8. Never drop an error silently: `_ = f()` only with a log next to it or a comment saying why it cannot fail.
9. No cleverness: generics only for containers, no `init()`, no package-level mutable variables, no callbacks in port signatures. An exception needs a comment saying why.
10. When you replace something, delete the old query, constant, text or error. One value, one constant (bot name, folder name, 20 MB); a copy forced by layering says so in a comment. Update `docs/` in the same PR when packages, tables or flow change.

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
- Branch names in Latin (ASCII) only, never Cyrillic, e.g. `49-sweeper-stuck-uploads`.
- Stay inside the task. Spotted a problem elsewhere — mention it in the PR, don't fix it.
- Unsure where code belongs — ask; don't invent a layer.
- Read only the files you need; use Grep/Glob instead of reading whole directories.
- Keep command output short: `go test ./path/...` for the touched package first, then `make test`.
- Code comments in English. User-facing texts in Russian, in `internal/<bot>/telegram/texts.go`.