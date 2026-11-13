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

## Деплой

Railway, сервис `files-bot`, окружения `dev` и `production`. Автодеплой из GitHub в Railway отключён — раскатывают только GitHub Actions через Railway CLI.

- **dev** — автоматически после merge в `main`: workflow `Deploy dev` гонит `Tests` и `Linters`, затем `railway up`.
- **prod** — вручную: Actions → **Deploy prod** → Run workflow → указать `ref` (ветка/тег/SHA, по умолчанию `main`) → подтвердить запуск в environment `production`.

Миграции применяются при старте сервиса.

### Откат

- Railway → сервис → Deployments → **Redeploy** предыдущего деплоя, или
- Actions → **Deploy prod** с прежним `ref` (тег или SHA).

⚠️ Миграции назад не откатываются: старая версия должна работать с текущей схемой БД.

### Секреты и переменные (GitHub → Settings → Environments)

| Environment | Тип | Имя | Значение |
|---|---|---|---|
| `dev` | secret | `RAILWAY_TOKEN` | Project token Railway для окружения `dev` |
| `dev` | variable | `RAILWAY_SERVICE` | `files-bot` |
| `production` | secret | `RAILWAY_TOKEN` | Project token Railway для окружения `production` |
| `production` | variable | `RAILWAY_SERVICE` | `files-bot` |

В `production` включены **Required reviewers**.

### Публичные страницы

Сервис отдаёт `/`, `/privacy` и `/terms` (нужны для верификации Google OAuth). Контактный адрес на них задаёт переменная `CONTACT_EMAIL`: в `prod` обязательна, в `dev` по умолчанию `contact@example.com`. Реальный адрес в репозиторий не коммитить.

### Корневая папка на Drive

Имя корневой папки задаёт переменная `DRIVE_ROOT_FOLDER` (по умолчанию «Most Docs»). В Railway `dev` задать `DRIVE_ROOT_FOLDER=Most Docs Test`, в `production` не задавать.

⚠️ После смены имени очистить кеш папок `drive_folders` в БД того окружения, где сменили имя (локальной; для Railway `dev` — в его БД): `DELETE FROM drive_folders;`. Иначе закэшированные id категорий указывают в старое дерево.

## Команды

```bash
make help   # список всех целей
```
