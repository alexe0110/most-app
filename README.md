# Most

Серия Telegram-ботов на общем Go-ядре. Первый бот — **Most Docs**: сохраняет документы пользователя на его Google Drive.

Архитектура, конвенции и модель данных — в [`docs/`](docs/) и [`CLAUDE.md`](CLAUDE.md).

## Требования

- Go 1.27+
- Docker (для Postgres и integration-тестов)
- `golangci-lint` v2 — линтеры
- опционально: `goose` и `sqlc` CLI — для `make migrate` / `make sqlc`

```bash
go install github.com/pressly/goose/v3/cmd/goose@latest
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
```

## Переменные окружения

Источник истины — **переменные окружения**; приложение читает только их. `.env` сам по себе не загружается — это удобный локальный способ задать переменные.

Скопируй шаблон и заполни значения:

```bash
cp .env.example .env
# отредактируй .env под себя
```

Загрузи `.env` в текущее окружение:

```bash
set -a; source .env; set +a
```

## Запуск

```bash
make db-up    # поднять Postgres в Docker
make run      # запустить сервис (миграции применяются при старте)
```

## База данных

Миграции (`goose`, `migrations/*.sql`) применяются **автоматически при старте**. Вручную:

```bash
make migrate  # применить миграции к $DATABASE_URL
make sqlc     # перегенерировать код из SQL после правок запросов
```

Запросы — в `internal/docsbot/postgres/queries/*.sql`; сгенерированный код — только в `internal/docsbot/postgres`.

## Тесты

```bash
make test       # unit — быстрые, без Docker и сети
make test-int   # integration — поднимает Postgres в testcontainers (нужен Docker)
make test-e2e   # e2e против dev-стенда (заглушка)
```

## Линтеры

```bash
make lint   # golangci-lint, включая depguard (правила зависимостей слоёв)
```

Перед push: `make lint && make test` должны быть зелёными.

## Команды

```bash
make help   # список всех целей
```
