# CLAUDE.md — Most

Most is a series of Telegram bots built on a shared Go core. The first bot, Most Docs, saves a user's documents to their own Google Drive.

Before touching code, read `docs/code_conventions.md`. Product design: `docs/README.md`. Tasks: GitHub Issues (milestone MVP).

## Architecture in 5 lines

- `internal/<bot>/` — domain: entities, `Service` with use cases, port interfaces in `ports.go`.
- `internal/<bot>/<adapter>/` — bot adapters: `telegram/` (inbound), `jobs/` (inbound), `postgres/` (outbound).
- `internal/platform/` — shared core for all bots: config, log, db, jobs, tg, oauth, gdrive, httpx, billing.
- `cmd/<bot>/main.go` — manual dependency wiring, no DI frameworks.
- Use cases return the meaning of a reply (`docs.Reply`); the channel adapter renders buttons and texts.

## Hard rules

1. The domain (`internal/docs`) **must not import** adapters, `platform/tg`, `platform/db`, `platform/gdrive` or external SDKs. Enforced by depguard.
2. Bots never import each other. `platform` never imports a bot.
3. Interfaces are declared by the consumer. Add an interface only when there is a second implementation or a test needs a fake.
4. `ctx context.Context` is the first argument of every function that does I/O. Never store it in a struct.
5. Errors: wrap with `fmt.Errorf("...: %w", err)`; adapters translate their errors into domain errors; log once, at the top (handler or worker).
6. SQL lives only in `.sql` files for `sqlc`, inside the `postgres/` adapter. Migrations: `goose`, in `migrations/`.
7. Never log tokens, document contents, file captions, IIN or document numbers.
8. No `utils`, `common`, `helpers` or `models` packages.
9. Do not add dependencies to `go.mod` unless explicitly asked.

## Adding a feature

1. Entities / errors / `Reply` types in the domain, if needed.
2. A `Service` method + a unit test (table-driven, fakes in `fakes_test.go`).
3. A port in `ports.go`, if the domain needs a new external action.
4. The port implementation in an adapter + an integration test (`//go:build integration`).
5. Rendering of the `Reply` in the inbound adapter (`telegram/`).
6. Wiring in `cmd/<bot>/main.go`, if a new dependency appeared.

Reference feature: document intake — `docs.Service.Receive` and `docs/telegram/receive.go`. Build new features the same way.

## Commands

```bash
make run          # local run, long polling, dev bot
make test         # unit tests
make test-int     # integration tests (needs Docker)
make lint         # golangci-lint, including depguard
make migrate      # goose up
make sqlc         # regenerate queries
```

Before saying "done": `make lint && make test` must pass.

## Working style

- Small changes: one task, one PR linking the issue (`Closes #N`).
- Do not touch code outside the task. If you spot a problem, describe it in the PR instead of silently fixing it.
- Unsure where code belongs — ask; do not invent a new layer.
- Code comments in English. User-facing texts in Russian, in `internal/<bot>/telegram/texts.go`.