# План: основной сценарий Most Docs (#9, #10, #12, #13, #14)

Первый настоящий домен `docsbot`. Делаем его эталоном: домен в центре, Telegram и
Postgres — адаптеры. Drive (#11) не реализуем, но оставляем шов: порт `Drive`, который
пока отвечает «не подключён», и поле `backup_status`.

## Порядок

1. Миграция `0003` (`backup_status`, `kind`, check `status in ('pending','saved')`) + запросы + `sqlc generate` — фундамент для всего.
2. Домен (#10): сущности, порты, Reply-типы, `Service` с юзкейсами, unit-тесты с фейками.
3. Адаптер `postgres`: реализация порта `Repo` (трансляция sqlc ↔ домен) + integration-тесты.
4. Адаптер `telegram`: роутер, приём файла и категории (#10), карточка (#12), `/list` (#13), поиск (#14), онбординг (#9).
5. Сборка в `main.go`, `setMyCommands`, заглушка `Drive`.
6. `make lint && make test && make test-int`, самопроверка, PR.

## Доменная модель (`internal/docsbot`)

**`document.go` — сущности**
- `UserID`, `DocumentID` — именованные строковые типы (UUID наружу).
- `Category string` + `AllCategories` (слоги `passport…other`), `Valid()`, `ParseCategory`. Русские подписи — в адаптере (`texts.go`).
- `Status` (`pending`→`saved`), `BackupStatus` (`none/pending/uploading/backed_up/failed`), `Kind` (`photo`/`document`).
- `Document{ID,UserID,Status,Category,Title,Kind,TgFileID,TgFileUniqueID,DriveFileID,BackupStatus,CreatedAt}`, метод `InDrive()`.
- `IncomingFile{UserID,Kind,FileID,FileUniqueID,Title}` — то, что пришло из Telegram.

**`ports.go` — интерфейсы домена**
- `Repo`: `CreateDocument`, `DocumentByFileUniqueID`, `DocumentByID` (скоуп по user), `SaveDocument` (категория+`saved`), `SetBackupStatus`, `SetTitle`, `SetCategory`, `DeleteDocument`, `CountSaved`, `CountNotBackedUp`, `CategoryCounts`, `ListByCategory(limit,offset)`, `Search(query,limit)`.
- `Drive`: `Connected(ctx, UserID) (bool, error)` — пока заглушка `false`; #11 заменит реализацией поверх `integrations` + постановкой `upload_document`.

**`reply.go` — смысл ответа, без UI**
- `Reply` (интерфейс) для ветвящихся юзкейсов: `NeedCategory{Doc}`, `AlreadySaved{Doc}`, `Saved{Doc,OfferDrive}`.
- Конкретные для остальных: `Card{Doc}`, `Catalog{Counts,NotInDrive,DriveConnected}`, `CategoryPage{Category,Docs,Page,Pages,Total,DriveConnected}`, `Found{Query,Docs}`.

**`service.go` — юзкейсы**
- `Receive` → дубль `saved`=`AlreadySaved`; `pending`=`NeedCategory`; новый → `CreateDocument(pending)` + `NeedCategory`.
- `ChooseCategory` → идемпотентно; `SaveDocument` (категория + `saved`); если `Drive.Connected` — `SetBackupStatus(pending)` (постановку задачи добавит #11); `OfferDrive` = не подключён и `CountSaved ∈ {1,5}`.
- `Card`, `Rename`, `Recategorize`, `Delete` (все со скоупом по user), `Catalog`, `CategoryPage` (пагинация по 10), `Search`.

**`errors.go`** — `ErrNotFound`, `ErrUnknownCategory`.

## Миграция и запросы

`migrations/0003_documents_backup.sql`:
- `alter table documents add column kind text not null default 'document' check (kind in ('photo','document'))`, затем снять default;
- `add column backup_status text not null default 'none' check (backup_status in ('none','pending','uploading','backed_up','failed'))`;
- заменить `documents_status_check` на `check (status in ('pending','saved'))`.

Запросы (`queries/documents.sql`) под `sqlc generate`: `Create/GetByFileUniqueID/GetByID/SaveDocument/SetBackupStatus/SetTitle/SetCategory/Delete/CountSaved/CountNotBackedUp/CategoryCounts/ListByCategory/Search`. Поиск — `ILIKE` + `similarity(title,$)>0.3` (pg_trgm), чтобы ловить опечатки.

## Адаптер telegram

Порядок хендлеров (первый совпавший выигрывает): команды (`/start /help /list /convert /cancel`) → конвертер (сессия) → колбэки документов (`cat:`,`doc:`,`list:`) → файловые сообщения (фото/документ) → текст (переименование, иначе поиск).

- Приём файла → `Receive` → кнопки категорий `cat:<docID>:<slug>`.
- Выбор категории → заменяем кнопки на «✅ <категория>» (повторный тап мёртв) + карточка `Card`.
- Карточка: «Прислать файл» (`doc:send:` — `sendPhoto`/`sendDocument` по `Kind`), «Переименовать» (force reply, состояние в памяти), «Сменить категорию», «Удалить» (подтверждение); «Открыть в Drive» — только `InDrive()`. После действия кнопки гасим (edit markup).
- `/list` → категории с числами → страница категории (пагинация) → «Прислать файл» у документа. 📥 и «N ещё не в Drive [Перенести]» — только если `DriveConnected` (сейчас нет).
- Поиск: любой текст вне сценариев → `Search` → список с «Прислать»; пусто → подсказка про `/list`.
- Онбординг (#9): `/start` (без обязательного Drive), `/start connected` (deep link после OAuth → «Drive подключён»), `/help`, `setMyCommands` при старте, кнопка «Подключить Google Drive» (URL из `oauth.AuthURL` через порт-адаптер), предложение подключить после 1-го и 5-го документа.
- Даты в карточке/списке — `models.Message` без нативного `date_time`; показываем текстом (fallback).

## Тесты

- **unit** (`service_test.go` + `fakes_test.go`): table-driven на все юзкейсы с фейками `Repo`/`Drive` (новый/дубль/pending, offer на 1-м и 5-м, ветка connected, пагинация, скоуп по user).
- **handler** (`telegram`, fake Bot API через `bot.WithServerURL`+`httptest`, фейковый `Service`): приём → категория → карточка; `/start`; поиск.
- **integration** (`postgres`, `dbtest.New`): create/дубль/by-id/save/list+пагинация/search(опечатка)/counts/update/delete.

## Решения и риски

- **Drive — заглушка `false`** (по заданию): ветки «подключён» живут и покрыты unit-тестами, но в проде не срабатывают; #11 включит реальный порт и постановку `upload_document`. Перенос N документов после OAuth — тоже #11.
- **`mime_type`/`size` не храним** (в `model.md` их нет; #10 упоминает — противоречие, следуем `model.md`). Решение о «>20 МБ» живёт в выгрузке — #11.
- **`kind`** добавлен: без него нельзя корректно вернуть оригинал («Прислать файл»). Обновляю `model.md`.
- **Поиск по категории** на MVP покрыт поиском по `title` (слоги категорий не совпадают с русским вводом); отмечу в PR.
- Скоуп всех чтений/действий по `user_id` — чужой документ по подделанному callback недоступен.

Код Drive, оплаты, LLM, inline и конвертера (кроме существующего `/convert`) — вне скоупа.
