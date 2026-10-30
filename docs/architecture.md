# Архитектура

Система в работе: из чего состоит, с чем общается и как ходят данные.

## MVP

### Система

Один сервис `docsbot` на Railway: в одном процессе вебхук Telegram, HTTP-эндпоинт OAuth callback и воркеры очереди River. Состояние — только в Postgres, файлы — на Drive пользователя.

```mermaid
flowchart LR
    subgraph CL["Пользователь"]
        TG["Telegram"]
        BR["Браузер"]
    end

    subgraph SVC["docsbot · Railway"]
        WH["Вебхук"]
        CB["OAuth callback"]
        WK["Воркеры River"]
    end

    subgraph EXT["Хранение и Google"]
        PG[("Postgres<br/>данные + River")]
        GO["Google OAuth"]
        GD["Google Drive"]
    end

    TG <-->|апдейты, ответы| WH
    BR -->|code + state| CB
    WH --> PG
    CB --> PG
    WK <--> PG
    CB -->|токены| GO
    WK -->|файлы| GD
    WK -.->|скачать файл| TG
```

### Процессы внутри docsbot

| Процесс | Что делает |
| --- | --- |
| Вебхук | Принимает апдейт, проверяет секрет, быстро отвечает 200. Лёгкие действия (кнопки, поиск, каталог) делает сам, тяжёлые ставит в River. |
| OAuth callback | Проверяет `state`, обменивает code на токены, сохраняет зашифрованный refresh token, пишет юзеру в Telegram «Drive подключён». |
| Воркеры River | `upload_document`: скачать из Telegram → загрузить на Drive → обновить статус → отправить карточку. Ретраи с backoff. |
| Периодические задачи River | `purge_users`: раз в сутки вычищает юзеров с `deleted_at`. |

Локально вместо вебхука — long polling, остальное так же.

### Подключение Google Drive

```mermaid
sequenceDiagram
    actor U as Юзер
    participant T as Telegram
    participant S as docsbot
    participant B as Браузер
    participant G as Google OAuth

    U->>T: /start
    T->>S: update
    S->>S: state = подпись(tg_user_id, TTL 10 мин) + PKCE
    S->>U: кнопка «Подключить Google Drive» (ссылка с state)
    U->>B: открывает ссылку
    B->>G: экран согласия, scope drive.file
    G->>B: redirect с code + state
    B->>S: GET /oauth/google/callback
    S->>S: проверить state
    S->>G: code + PKCE verifier → токены
    S->>S: зашифровать refresh token → integrations
    S->>B: «Готово, вернитесь в Telegram»
    S->>T: «Drive подключён, пришлите документ»
```

### Загрузка документа

```mermaid
sequenceDiagram
    actor U as Юзер
    participant T as Telegram
    participant W as Вебхук
    participant DB as Postgres
    participant K as Воркер River
    participant D as Google Drive

    U->>T: фото / файл
    T->>W: update
    W->>DB: есть такой tg_file_unique_id?
    alt уже сохранён
        W->>U: «Уже сохранён» + ссылка
    else новый
        W->>DB: documents: pending
        W->>U: кнопки категорий
        U->>W: выбрал категорию
        W->>DB: uploading + задача upload_document (одна транзакция)
        W->>U: «Сохраняю…»
        K->>DB: взял задачу
        K->>T: getFile, скачать в память
        K->>D: загрузить в Most Docs/категория
        K->>DB: saved + drive_file_id
        K->>U: карточка: имя, папка, ссылка
    end
```

Задача ставится в River в той же транзакции, что и смена статуса: либо оба изменения есть, либо нет ни одного.

### Надёжность

| Ситуация                                             | Что происходит                                                                                                                            |
|------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Telegram повторил апдейт (вебхук не ответил вовремя) | Дубль отсекается по `unique (user_id, tg_file_unique_id)`; повторное нажатие кнопки на уже обработанном документе игнорируется по статусу |
| Drive или Telegram недоступен                        | Ретраи River с backoff; после последней попытки — `failed` и сообщение «Не удалось сохранить, попробовать ещё раз?»                       |
| Юзер отозвал доступ в Google                         | `invalid_grant` → `integrations.status = revoked` → предложение переподключить Drive                                                      |
| Рестарт или деплой сервиса                           | Задачи River в Postgres не теряются, незавершённые подхватываются после старта                                                            |
| Падение воркера на задаче                            | River вернёт задачу в очередь по таймауту                                                                                                 |

### Деплой

| Что       | Как                                                                                          |
|-----------|----------------------------------------------------------------------------------------------|
| Хостинг   | Railway: сервис `docsbot` + Postgres                                                         |
| Масштаб   | Один инстанс, вебхук и воркеры в одном процессе. Делить на два сервиса — только при нагрузке |
| Окружения | `dev` (свой бот `@most_docs_dev_bot`, своя БД) и `prod`                                      |
| Секреты   | Переменные окружения Railway: токен бота, Google client, ключ шифрования, секрет вебхука     |
| Миграции  | `goose` при старте сервиса; таблицы River — его миграциями                                   |
| Деплой    | Пуш в `main` → CI → Railway                                                                  |

## Дальше

> Заглушка. LLM vision для распознавания (v2), другие хранилища (v2), Stars (v3), напоминания (v3+), WhatsApp и Most Calendar на том же ядре.