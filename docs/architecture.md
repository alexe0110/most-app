# Архитектура

Система в работе: из чего состоит, с чем общается и как ходят данные.

## MVP

### Система

Один сервис `docsbot` на Railway: в одном процессе вебхук Telegram, HTTP-эндпоинт OAuth callback и воркеры очереди River. Состояние — только в Postgres. Файлы остаются в Telegram (храним `file_id`); при подключённом Drive копия выгружается на Диск пользователя фоновой задачей.

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
| Вебхук | Принимает апдейт, проверяет секрет, быстро отвечает 200. Лёгкие действия (кнопки, поиск, каталог, сохранение документа по `file_id`) делает сам; тяжёлое (выгрузка на Drive) ставит в River. |
| OAuth callback | Проверяет `state`, обменивает code на токены, сохраняет зашифрованный refresh token, пишет юзеру в Telegram «Drive подключён». |
| Воркеры River | `upload_document` (бэкап в Drive): скачать из Telegram → загрузить на Drive → `backup_status = backed_up` + `drive_file_id`. Ставится только при подключённом Drive; при подключении — на каждый уже сохранённый документ. Ретраи с backoff. |
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

    U->>T: «Подключить Drive» (после документа или /settings)
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

После подключения бот спрашивает «Перенести N сохранённых документов в Drive? [Да] [Нет]» («Да» по умолчанию) и ставит их выгрузку в River. Новые документы после этого выгружаются автоматически.

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
        W->>U: «Уже сохранён»
    else новый
        W->>DB: documents: pending (tg_file_id)
        W->>U: кнопки категорий
        U->>W: выбрал категорию
        W->>DB: saved (сразу, без очереди)
        W->>U: карточка: имя, категория
        opt Drive подключён
            W->>DB: backup_status = pending + задача upload_document (одна транзакция)
            K->>DB: взял задачу
            K->>T: getFile, скачать в память
            K->>D: загрузить в Most Docs/категория
            K->>DB: backup_status = backed_up + drive_file_id
            K->>U: «Документ в Drive» + ссылка
        end
    end
```

Документ сохраняется сразу после выбора категории (в Telegram по `file_id`), без очереди. Бэкап на Drive — отдельный путь: только при подключённом Drive, задача ставится в River в той же транзакции, что и `backup_status`.

### Надёжность

| Ситуация                                             | Что происходит                                                                                                                            |
|------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------|
| Telegram повторил апдейт (вебхук не ответил вовремя) | Дубль отсекается по `unique (user_id, tg_file_unique_id)`; повторное нажатие кнопки на уже обработанном документе игнорируется по статусу |
| Drive не подключён                                   | Документ хранится в Telegram по `file_id`, в каталоге помечен 📥; выгрузка — после подключения Drive                                      |
| Токен бота утёк или бот удалён                        | Доступ к файлам в Telegram теряется; подключённый Drive остаётся надёжной копией. Токен — только в env, при утечке Revoke в BotFather     |
| Drive недоступен при бэкапе                           | Ретраи River с backoff; после последней попытки — `backup_status = failed` и предложение повторить. Документ остаётся доступен в Telegram |
| Юзер отозвал доступ в Google                         | `invalid_grant` → `integrations.status = revoked` → предложение переподключить Drive                                                      |
| Рестарт или деплой сервиса                           | Задачи River в Postgres не теряются, незавершённые подхватываются после старта                                                            |
| Падение воркера на задаче                            | River вернёт задачу в очередь по таймауту                                                                                                 |

### Деплой

| Что       | Как                                                                                          |
|-----------|----------------------------------------------------------------------------------------------|
| Хостинг   | Railway: сервис `docsbot` + Postgres                                                         |
| Масштаб   | Один инстанс, вебхук и воркеры в одном процессе. Делить на два сервиса — только при нагрузке |
| Окружения | `dev` (свой бот `@most_files_dev_bot`, своя БД) и `prod`                                      |
| Секреты   | Переменные окружения Railway: токен бота, Google client, ключ шифрования, секрет вебхука     |
| Миграции  | `goose` при старте сервиса; таблицы River — его миграциями                                   |
| Деплой    | Пуш в `main` → CI → Railway                                                                  |

## Дальше

> Заглушка. LLM vision для распознавания (v2), другие хранилища (v2), Stars (v3), напоминания (v3+), WhatsApp и Most Calendar на том же ядре.