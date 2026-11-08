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
		"Подключишь Drive — бот будет видеть только файлы, которые сам создал."

	textHelp = "Что я умею:\n" +
		"• Приём документов — пришли фото, PDF или файл и выбери категорию.\n" +
		"• /list — твои документы по категориям.\n" +
		"• Поиск — напиши слово из названия.\n" +
		"• /convert — фото → PDF или сжать изображение.\n\n" +
		"Цель — перенести документы на твой Google Drive; пока они хранятся в Telegram."

	textDriveConnected = "Google Drive подключён ✅ Новые документы будут попадать на твой Диск."

	textChooseCategory = "Выбери категорию:"
	textAlreadySaved   = "Этот файл уже сохранён."
	textSaved          = "Сохранил ✅"

	textRenamePrompt = "Пришли новое название документа."
	textRenameEmpty  = "Название пустое. Нажми «Имя» и пришли название ещё раз."

	textDeleteConfirm   = "Удалить этот документ? Отменить будет нельзя."
	textDeleted         = "Документ удалён 🗑"
	textDeleteCancelled = "Удаление отменено."
	textGone            = "Этого документа уже нет."

	// Callback toasts.
	toastSending = "Отправляю…"
	toastDone    = "Готово"

	textCatalogEmpty = "Пока пусто — пришли документ, и он появится здесь."
	textNothingFound = "Ничего не нашёл. Загляни в /list или пришли новый документ."

	textOfferDrive = "Хочешь, чтобы документы лежали на твоём Google Drive? Подключи — " +
		"новые файлы будут попадать туда автоматически, а бот увидит только то, что сам создал."

	textTransferOffer    = "Перенести %d сохранённых документов в Google Drive?"                 // %d = count
	textTransferStarted  = "Начинаю перенос %d документов в Drive — пришлю, когда будут готовы." // %d = count
	textTransferDeclined = "Хорошо. Перенести можно позже — в /list или /settings."
	textBackedUp         = "☁️ «%s» теперь в Google Drive." // %s = title
	textBackupFailed     = "Не удалось выгрузить документ в Google Drive. Попробовать ещё раз?"
	textReconnect        = "Доступ к Google Drive отозван. Подключи заново, чтобы продолжить выгрузку."
	textRetryQueued      = "Повторяю выгрузку в Google Drive."
	textStatusHeader     = "Загрузка в Drive (%d из %d)" // done, total
	textStatusMore       = "…и ещё %d"                   // %d = hidden lines

	textUnknown    = "Не понимаю эту команду. Я умею /start, /list, /convert и /help."
	textError      = "Что-то пошло не так. Попробуй ещё раз."
	textSendFailed = "Не удалось отправить файл. Попробуй ещё раз."
)

// Settings, disconnect, export and delete texts (#15).
const (
	textDisconnected = "Google Drive отключён. Новые документы останутся в Telegram, " +
		"а уже выгруженные файлы и ссылки на них сохранены."
	textRetryAllQueued = "Повторяю выгрузку в Google Drive: %d шт." // %d = count
	textNoFailed       = "Документов с ошибкой выгрузки нет."

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
