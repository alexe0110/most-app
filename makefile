.PHONY: help run test test-int test-e2e lint db-up migrate sqlc


help:  ## список команд
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-12s %s\n", $$1, $$2}'

run:    ## Запуск сервиса
	go run ./cmd/docsbot

test:       ## unit-тесты (без Docker)
	go test ./...

test-int:   ## integration-тесты (нужен Docker)
	go test -tags=integration -count=1 ./...

test-e2e:   ## e2e-тесты (заглушка)
	go test -tags=e2e -count=1 ./tests/e2e/...

lint:   ## Линтеры
	golangci-lint run

db-up:  ## Поднять postgres
	docker compose up -d postgres

migrate:
	@echo "TODO: goose (#5)"

sqlc:
	@echo "TODO: sqlc (#5)"