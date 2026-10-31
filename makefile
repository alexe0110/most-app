.PHONY: run test lint db-up db-down migrate sqlc


help:  ## список команд
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-12s %s\n", $$1, $$2}'

run:    ## Запуск сервиса
	go run ./cmd/docsbot

test:   ## Запуск тестов
	go test ./...

lint:   ## Линтеры
	golangci-lint run

db-up:  ## Поднять postgres
	docker compose up -d postgres

migrate:
	@echo "TODO: goose (#5)"

sqlc:
	@echo "TODO: sqlc (#5)"