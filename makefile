.PHONY: help run test test-int test-cover test-e2e lint db-up migrate sqlc


help:  ## список команд
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-12s %s\n", $$1, $$2}'

run:    ## Запуск сервиса
	go run ./cmd/docsbot

test:       ## unit-тесты: -race и случайный порядок (без Docker)
	go test -race -shuffle=on -count=1 ./...

test-int:   ## integration-тесты (нужен Docker)
	go test -tags=integration -count=1 ./...

# Coverage of internal/ by unit and integration tests together: go test merges the
# blocks of all test binaries into one profile. Generated and test-support code is left out.
test-cover:   ## покрытие unit + integration по пакетам, итог в coverage.out (нужен Docker)
	go test -tags=integration -count=1 -coverpkg=./internal/... -coverprofile=coverage.out ./...
	@awk 'NR > 1 && $$1 !~ /sql\.go|\/docsbottest\/|\/dbtest\// { \
		pkg = $$1; sub(/:.*/, "", pkg); sub(/^github.com\/alexe0110\/most-app\//, "", pkg); sub(/\/[^\/]+$$/, "", pkg); \
		if (!($$1 in stmts)) { stmts[$$1] = $$2; owner[$$1] = pkg } \
		if ($$3 > hits[$$1]) hits[$$1] = $$3 } \
	END { \
		for (b in stmts) { all[owner[b]] += stmts[b]; if (hits[b] > 0) hit[owner[b]] += stmts[b] } \
		printf "%-45s %7s %9s\n", "package", "stmts", "covered"; \
		for (p in all) { printf "%-45s %7d %8.1f%%\n", p, all[p], 100 * hit[p] / all[p] | "sort -k1,1"; total += all[p]; covered += hit[p] } \
		close("sort -k1,1"); \
		printf "%-45s %7d %8.1f%%\n", "total", total, 100 * covered / total }' coverage.out

test-e2e:   ## e2e-тесты (заглушка)
	go test -tags=e2e -count=1 ./tests/e2e/...

lint:   ## Линтеры
	golangci-lint run

db-up:  ## Поднять postgres
	docker compose up -d postgres

migrate:  ## Применить миграции goose (нужен goose CLI и $DATABASE_URL)
	@test -n "$(DATABASE_URL)" || { echo "DATABASE_URL не задан — выполни: set -a; source .env; set +a"; exit 1; }
	goose -dir migrations postgres "$(DATABASE_URL)" up

sqlc:  ## Сгенерировать код из SQL (нужен sqlc CLI)
	sqlc generate