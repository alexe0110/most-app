package telegram

// User-facing texts. Russian, kept in one place per project convention. Tone
// (product.md): documents are "хранятся в Telegram" / "ещё не в Drive", the
// goal is the user's Google Drive; never "временно".

// Core flow texts (#9, #10, #12, #13, #14).
const (
	textStart = "Привет! Я Most Docs. Пришли документ — фото, PDF или файл, — " +
		"я разложу его по категориям и найду по запросу.\n\n" +
		"Цель — хранить документы на твоём Google Drive. Пока Drive не подключён, " +
		"они лежат во входящей папке в Telegram — можно начать без настройки.\n\n" +
		"Подключишь Drive — бот будет видеть только файлы, которые сам создал. " +
		"Он создаёт свою папку «Most Docs» на твоём Диске и не видит папки, которые ты создаёшь сам, " +
		"поэтому если у тебя уже есть папка с таким именем — появится вторая."

	textHelp = "Что я умею:\n" +
		"• Приём документов — пришли фото, PDF или файл и выбери категорию.\n" +
		"• /list — твои документы по категориям.\n" +
		"• Поиск — напиши слово из названия.\n" +
		"• /convert — фото → PDF или сжать изображение.\n\n" +
		"Цель — перенести документы на твой Google Drive; пока они хранятся в Telegram."

	textDriveConnected = "Google Drive подключён ✅ Новые документы будут попадать на твой Диск."

	textChooseCategory      = "Выбери категорию:"
	textChooseCategoryGroup = "Выбери категорию для %d %s:" // files in the album, plural("файл")
	textChooseCategoryAlbum = "Выбери категорию для альбома:"
	textAlbumChoice         = "Альбом: %d %s. Что сделать?"                        // count, plural("изображение")
	textAlbumDuplicates     = "Уже были сохранены: %d"                             // %d = duplicate files
	textGroupSaved          = "Сохранил %d %s в категории «%s»."                   // count, plural("файл"), category
	textGroupTooLarge       = "Слишком большие для Drive, остались в Telegram: %d" // %d = files
	textAlreadySaved        = "Этот файл уже сохранён."

	textRenamePrompt = "Пришли новое название документа."
	textRenameEmpty  = "Название пустое. Нажми «Имя» и пришли название ещё раз."

	textDeleteConfirm   = "Удалить этот документ? Отменить будет нельзя."
	textDeleted         = "Документ удалён 🗑"
	textDeleteCancelled = "Удаление отменено."
	textGone            = "Этого документа уже нет."
	textStale           = "Кнопка устарела, открой заново."

	// Callback toasts.
	toastSending = "Отправляю…"
	toastDone    = "Готово"
	// toastNotRetryable: the upload is no longer failed (double tap or stale button).
	toastNotRetryable = "Уже в работе или готово"

	textCatalogEmpty = "Пока пусто — пришли документ, и он появится здесь."
	textNothingFound = "Ничего не нашёл. Загляни в /list или пришли новый документ."
	textSearchHint   = "Напиши, что искать: часть названия документа."

	textOfferDrive = "Хочешь, чтобы документы лежали на твоём Google Drive? Подключи — " +
		"новые файлы будут попадать туда автоматически, а бот увидит только то, что сам создал."

	textTransferOffer     = "Перенести %d сохранённых документов в Google Drive?" // %d = count
	textNothingToTransfer = "Всё уже в Google Drive."
	textTransferDeclined  = "Хорошо. Перенести можно позже — в /list или /settings."
	textBackedUp          = "☁️ «%s» теперь в Google Drive." // %s = title
	textBackupFailed      = "Не удалось выгрузить документ в Google Drive. Попробовать ещё раз?"
	textReconnect         = "Доступ к Google Drive отозван. Подключи заново, чтобы продолжить выгрузку."
	textRetryQueued       = "Повторяю выгрузку в Google Drive."
	textStatusHeader      = "Загрузка в Drive (%d из %d)" // done, total
	textStatusMore        = "…и ещё %d"                   // %d = hidden lines
	textStatusListHint    = "✏️ Изменить имя и категорию: /list"
	textTransferHeader    = "Перенос в Drive (%d из %d)" // done, total
	textTransferAllDone   = "Готово: %d в Drive"         // %d = backed up

	textStoragePending      = "⏳ Загружаю в Drive…"
	textStorageInDrive      = "☁️ В Google Drive"
	textStorageFailed       = "❌ Не удалось выгрузить в Drive"
	textStorageTooLarge     = "📥 Слишком большой для Drive"
	textDriveFileStayed     = "В Drive файл остался в прежней папке"
	textStorageTelegramOnly = "📥 Только в Telegram, ещё не в Drive"

	// textGroupNotice is sent once to a group the bot was added to, right before it leaves.
	textGroupNotice = "Я работаю только в личных сообщениях: документы не должны попадать в общий чат. Напиши мне в личку."
	textUnsupported = "Пока принимаю фото и файлы. Видео/голосовые — отправь как файл"
	// Intake limits: a refused file is not stored.
	textTooManyPending = "Сначала выбери категорию для предыдущих файлов"
	textRateLimited    = "Слишком много файлов сразу. Подожди минуту и пришли остальные."
	textQuotaExceeded  = "Достигнут лимит документов (%d). Удали ненужные, чтобы добавить новые." // %d = docsbot.MaxDocumentsPerUser
	textUnknown        = "Не понимаю эту команду. Я умею /start, /list, /convert и /help."
	textError          = "Что-то пошло не так. Попробуй ещё раз."
	textSendFailed     = "Не удалось отправить файл. Попробуй ещё раз."
)

// Settings, disconnect, export and delete texts (#15).
const (
	textDisconnected = "Google Drive отключён. Новые документы останутся в Telegram, " +
		"а уже выгруженные файлы и ссылки на них сохранены."
	textNoFailed = "Документов с ошибкой выгрузки нет."

	textExportEmpty     = "Пока нечего присылать — сохранённых документов нет."
	textExportStart     = "Присылаю твои документы…"
	textExportDone      = "Готово — прислал %d документов."                                                     // %d = count
	textExportTruncated = "Прислал %d из %d. Нажми «Прислать все документы» ещё раз, чтобы получить остальные." // sent, total

	textDeleteDataPrompt = "Это удалит все твои данные у бота: документы, категории и подключение Google Drive.\n\n" +
		"Файлы, уже выгруженные на твой Google Drive, останутся у тебя. Сообщения в этом чате бот удалить не может.\n\n" +
		"Советую сначала нажать «Прислать все документы».\n\n" +
		"Чтобы подтвердить, пришли слово УДАЛИТЬ."
	textDataDeleted = "Данные удалены. Файлы на Google Drive остались у тебя; сообщения в этом чате можно удалить вручную.\n\n" +
		"Напиши /start, чтобы начать заново."
	textDeleteWrongWord = "Удаление отменено — слово не совпало."
)

// Inline button labels for the core flow.
const (
	btnConnectDrive       = "☁️ Подключить Google Drive"
	btnSendFile           = "📤 Прислать файл"
	btnOpenDrive          = "☁️ Открыть в Drive"
	btnOpenFolder         = "📁 Папка в Drive"
	btnOpenCategoryFolder = "📁 Папка категории"
	btnRename             = "✏️ Имя"
	btnChangeCat          = "🗂 Категория"
	btnDelete             = "🗑 Удалить"
	btnDeleteYes          = "Да, удалить"
	btnDeleteNo           = "Отмена"
	btnTransfer           = "📤 Перенести в Drive"
	btnBack               = "⬅️ Назад"
	btnMyDocs             = "📂 Мои документы"
	btnBackToCats         = "⬅️ Категории"
	btnTransferYes        = "Да, перенести"
	btnTransferNo         = "Не сейчас"
	btnRetryBackup        = "🔁 Повторить"
	btnReconnect          = "☁️ Переподключить Google Drive"
	btnDisconnect         = "🔌 Отключить Google Drive"
	btnRetryAll           = "🔁 Повторить выгрузку"
	btnExportAll          = "📨 Прислать все документы"
	btnDeleteData         = "🗑 Удалить мои данные"
)

// Converter mode (/convert) texts.
const (
	textConvertStart = "Что сделать с изображениями?"

	textConvertPDFPrompt = "Пришли фото — по одному или альбомом. Когда закончишь, нажми «Готово».\n\n" +
		"Совет: отправляй снимки «как файл» — иначе Telegram сильно их сожмёт."

	textConvertCompressPrompt = "Пришли одно фото для сжатия.\n\n" +
		"Совет: отправляй «как файл» (до 20 МБ) — иначе Telegram уже ужмёт качество."

	textConvertChooseLimit  = "До какого размера сжать?"
	textConvertChooseFormat = "В каком формате сохранить?"
	textConvertCustomLimit  = "Введи лимит в килобайтах, например 300."

	textConvertBadNumber = "Нужно целое число килобайт, например 300."
	textConvertTapButton = "Нажми кнопку выше или /cancel, чтобы выйти."
	textConvertNoImages  = "Сначала пришли хотя бы одно фото."

	textConvertUnsupported    = "Так я пока не умею. Пришли фото в JPEG или PNG. HEIC, PDF и видео — позже."
	textConvertDownloadFailed = "Не удалось получить файл. Попробуй ещё раз."
	textConvertTooLarge       = "Изображение слишком большое. Пришли снимок поменьше."
	textConvertFailed         = "Не удалось обработать изображение. Проверь, что это JPEG или PNG."

	textConvertExpired   = "Сессия конвертера истекла. Начни заново: /convert."
	textConvertCancelled = "Режим конвертера выключен."
	textConvertNothing   = "Сейчас нечего отменять."

	textConvertMenu      = "Что сделать с файлом?"
	textConvertSaving    = "💾 Сохраняю…"
	textConvertResultOld = "Эта кнопка устарела. Преобразуй файл ещё раз."

	textAlbumTooMany = "В один PDF помещается не больше %d страниц. Сохрани фото по одному."
	textAlbumTooBig  = "Фото в альбоме больше 20 МБ вместе — в один PDF не поместятся. Сохрани их по одному."

	// textConvertBestEffort takes the reached size in KB (target was out of reach).
	textConvertBestEffort = "Меньше не вышло — сжал до %d КБ."
)

// Inline button labels for /convert.
const (
	btnPDF         = "📄 Фото → PDF"
	btnCompress    = "🗜 Сжать"
	btnDone        = "Готово"
	btnCancel      = "Отмена"
	btnLimit1MB    = "до 1 МБ"
	btnLimit2MB    = "до 2 МБ"
	btnLimit5MB    = "до 5 МБ"
	btnLimitCustom = "Свой лимит"
	btnJPEG        = "JPEG"
	btnPNG         = "PNG"
)

// Inline button labels for converting from a card, a picker and a result.
const (
	btnConvertCard   = "🛠 Преобразовать"
	btnConvertPicker = "🛠 Сжать / В PDF"
	btnQuick1MB      = "🗜 1 МБ"
	btnQuick2MB      = "🗜 2 МБ"
	btnQuick5MB      = "🗜 5 МБ"
	btnQuickPDF      = "📄 В PDF"
	btnQuickCancel   = "✖️ Отмена"
	btnSaveResult    = "💾 Сохранить в документы"
	btnSkipResult    = "⬇️ Только файл"
)

// Title suffixes of documents made by the converter.
const (
	suffixCompressed = " (сжатый)"
	suffixPDF        = " (PDF)"
)

// Inline button labels for an album of images.
const (
	btnAlbumPDF  = "📄 Собрать в один PDF"
	btnAlbumEach = "💾 Сохранить по одному"
)
