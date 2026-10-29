# Архитектура

> Набросок, не финал

Боты — тонкий слой над общим ядром: Telegram, OAuth, оплата, очередь и LLM живут в ядре один раз, каждый бот знает только свой домен. Каждый слой вызывает только нижний.

```mermaid
flowchart TB
    subgraph CH["Каналы"]
        TG["Telegram Bot API<br/>вебхук, файлы, Stars"]
        WA["WhatsApp<br/>позже"]
    end
    subgraph BOTS["Боты (домен)"]
        DOCS["Most Docs<br/>сохранение, каталог, поиск"]
        CAL["Most Calendar<br/>следующий бот"]
    end
    subgraph CORE["Ядро"]
        ctg["tg<br/>апдейты, i18n"]
        auth["auth<br/>OAuth, токены"]
        billing["billing<br/>Stars (v3)"]
        jobs["jobs<br/>очередь, планировщик"]
        store["store<br/>БД"]
        llm["llm<br/>vision (v2)"]
    end
    subgraph EXT["Внешнее"]
        PG[("Postgres")]
        GD["Google Drive API"]
        LLMAPI["LLM vision API"]
    end

    TG --> DOCS --> jobs --> PG

    classDef later stroke-dasharray: 4 4
    class WA,CAL later
```

## Процессы внутри одного бинарника

- **Вебхук.** Принимает апдейт, сразу отвечает Telegram, тяжёлую работу кладёт в очередь.
- **Воркеры.** Берут задачи из `jobs`: скачать файл из Telegram, загрузить на Drive (с v2 — распознать); ретраи с backoff.
- **Планировщик (v1).** Раз в час берёт `reminders` с наступившим `fire_at` и шлёт напоминания в пределах лимита Telegram.

## Обработка документа

```mermaid
sequenceDiagram
    actor U as Юзер
    participant T as Telegram
    participant W as Вебхук
    participant Q as jobs
    participant K as Воркер
    participant D as Google Drive

    U->>T: фото / файл
    T->>W: update
    W->>U: кнопки категорий
    U->>W: выбрал категорию
    W->>Q: задача upload
    W->>U: «сохраняю…»
    K->>Q: взял задачу
    K->>T: getFile, скачать
    K->>D: загрузить в Most Docs/категория
    K->>U: карточка: имя, папка, ссылка
```
