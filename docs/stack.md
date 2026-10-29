# Стек и деплой

> Набросок, не финал

Go-монорепо: один бинарник на бота, общие пакеты ядра, один Postgres на всех. Отдельных брокеров (Redis, Kafka) на старте не берём.

| Слой           | Выбор                                                                     |
|----------------|---------------------------------------------------------------------------|
| Язык           | Go                                                                        |
| Telegram       | `go-telegram/bot` или `telebot`                                           |
| Приём апдейтов | Вебхук на Railway, long polling локально                                  |
| Google         | `google.golang.org/api/drive/v3`, `golang.org/x/oauth2`                   |
| БД             | Postgres + `pgx`, запросы через `sqlc`, миграции `goose`                  |
| Очередь        | `river` (на Postgres) или своя `jobs` + `SELECT … FOR UPDATE SKIP LOCKED` |
| LLM (v2)       | Vision-модель по HTTP за интерфейсом `Extractor`, провайдер заменяемый    |
| Деплой         | Railway: сервис на бота + Postgres                                        |
| Наблюдаемость  | `slog` (JSON), таблица событий для продуктовых метрик, Sentry             |
| CI             | GitHub Actions: `go test`, `golangci-lint`, автодеплой из `main`          |
