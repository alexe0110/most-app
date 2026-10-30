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
| Очередь        | River (очередь задач на Postgres)                                         |
| LLM (v2)       | Vision-модель по HTTP за интерфейсом `Extractor`, провайдер заменяемый    |
| Деплой         | Railway: один сервис на бота (вебхук + воркеры) + Postgres                |
| Наблюдаемость  | `slog` (JSON), Sentry; продуктовые метрики (`events`) — с v1              |
| CI             | GitHub Actions: `go test`, `golangci-lint`, автодеплой из `main`          |
