package telegram

// User-facing texts. Russian, kept in one place per project convention.
const (
	startText = "Привет! Я Most Docs — сохраню твои документы в твой Google Drive.\n\n" +
		"Скоро подключим Drive и научимся принимать файлы."

	unknownText = "Не понимаю эту команду. Пока я умею только /start и /convert."

	textDriveConnected = "Google Drive подключён ✅ Пришлите документ — я сохраню его на ваш Диск."
)

// Converter mode (/convert) texts.
const (
	textConvertStart = "Что сделать с изображениями?"

	textConvertPDFPrompt = "Пришлите фото — по одному или альбомом. Когда закончите, нажмите «Готово».\n\n" +
		"Совет: отправляйте снимки «как файл» — иначе Telegram сильно их сожмёт."

	textConvertCompressPrompt = "Пришлите одно фото для сжатия.\n\n" +
		"Совет: отправляйте «как файл» (до 20 МБ) — иначе Telegram уже ужмёт качество."

	textConvertChooseLimit  = "До какого размера сжать?"
	textConvertChooseFormat = "В каком формате сохранить?"
	textConvertCustomLimit  = "Введите лимит в килобайтах, например 300."

	textConvertBadNumber = "Нужно целое число килобайт, например 300."
	textConvertTapButton = "Нажмите кнопку выше или /cancel, чтобы выйти."
	textConvertNoImages  = "Сначала пришлите хотя бы одно фото."

	textConvertUnsupported    = "Так я пока не умею. Пришлите фото в JPEG или PNG. HEIC, PDF и видео — позже."
	textConvertDownloadFailed = "Не удалось получить файл. Попробуйте ещё раз."
	textConvertTooLarge       = "Изображение слишком большое. Пришлите снимок поменьше."
	textConvertFailed         = "Не удалось обработать изображение. Проверьте, что это JPEG или PNG."

	textConvertExpired   = "Сессия конвертера истекла. Начните заново: /convert."
	textConvertCancelled = "Режим конвертера выключен."
	textConvertNothing   = "Сейчас нечего отменять."

	// textConvertBestEffort takes the reached size in KB (target was out of reach).
	textConvertBestEffort = "Меньше не вышло — сжал до %d КБ."
)

// Inline button labels.
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
