package converter

// User-facing texts of the converter. Russian, kept in one place per project
// convention.

// Texts the docs adapter also has. A copy, because the converter cannot import
// the telegram package.
const (
	textGone       = "Этого документа уже нет."
	textError      = "Что-то пошло не так. Попробуй ещё раз через минуту — твои документы на месте."
	textSendFailed = "Не удалось отправить файл. Попробуй ещё раз."
)

// textStale answers a press that no longer applies.
const textStale = "Эта кнопка уже не работает. Открой документ заново."

// Converter mode (/convert) texts.
const (
	textConvertStart = "Что сделать? Выйти — /cancel."
	// textConvertBusy answers a new /convert session while too many are open.
	textConvertBusy = "Сейчас много запросов. Попробуй через пару минут."

	textConvertPDFPrompt      = "Пришли фото — по одному или альбомом. Когда закончишь, нажми «Готово»."
	textConvertPDFAccepted    = "Добавил. В PDF уже %d %s — пришли ещё или нажми «Готово»." // pages so far, plural("страница")
	textConvertCompressPrompt = "Пришли фото, которое нужно сжать. Лучше файлом — так качество будет выше."
	textConvertPDFAwait       = "Пришли PDF-файл."
	textConvertPDFMenu        = "Что сделать с PDF?"

	// textConvertFileTip is added once per user, after they send a compressed photo.
	textConvertFileTip = "Совет: присылай снимки файлом (до 20\u00A0МБ) — так Telegram не сжимает фото."

	textConvertChooseLimit  = "До какого размера сжать?"
	textConvertChooseFormat = "В каком формате сохранить?"
	textConvertCustomLimit  = "Напиши размер в килобайтах, например 300."

	textConvertBadNumber = "Нужно целое число килобайт, например 300."
	textConvertTapButton = "Нажми кнопку выше или /cancel, чтобы выйти."
	textConvertNoImages  = "Сначала пришли хотя бы одно фото."

	textConvertUnsupported        = "С таким файлом не справлюсь. Пришли фото в JPG, PNG или WEBP."
	textConvertDownloadFailed     = "Не удалось получить файл. Попробуй ещё раз."
	textConvertTooLarge           = "Изображение слишком большое. Пришли снимок поменьше."
	textConvertFormatNotSupported = "Этот формат пока не поддерживается. Пришли JPG, PNG или WEBP."
	textConvertFailed             = "Не удалось обработать изображение. Проверь, что это JPG, PNG или WEBP."

	textConvertCancelled = "Вышел из конвертера. Новые файлы снова сохраняю в документы."
	textConvertNothing   = "Сейчас нечего отменять."

	textConvertProgress = "⏳ Обрабатываю…"
	textConvertDone     = "Готово ✅"

	textConvertMenu      = "Что сделать с файлом?"
	textConvertSaving    = "💾 Сохраняю…"
	textConvertResultOld = "Эта кнопка уже не работает. Преобразуй файл ещё раз."

	textReplaceConfirm = "Заменить оригинал этим файлом? Старую версию вернуть будет нельзя."
	textReplaceBusy    = "Подожди, файл ещё сохраняется."
	textReplaceMissing = "Файла нет на Google Диске. Сначала нажми «☁️ Загрузить заново» в карточке документа."

	textAlbumTooMany = "В один PDF помещается не больше %d страниц. Сохрани фото по одному."
	textAlbumTooBig  = "Фото в альбоме больше %d МБ вместе — в один PDF не поместятся. Сохрани их по одному."

	textPhotosTooMany = "В один PDF помещается не больше %d страниц. Это фото не добавил — нажми «Готово» или отмени."
	textPhotosTooBig  = "Фото вместе больше %d МБ — в один PDF не поместятся. Это фото не добавил — нажми «Готово» или отмени."

	textPhotosOverLimit = "Фото вместе больше %d МБ — в один PDF не поместятся. Начни заново с меньшим числом фото."

	textPDFKeepPrompt    = "Какие страницы оставить? Например: 1-3, 5"
	textPDFDropPrompt    = "Какие страницы убрать? Например: 1-3, 5"
	textPDFBadRange      = "Не понял, какие страницы. Напиши, например: 1-3,5. Нельзя оставить файл без страниц."
	textPDFMergePrompt   = "Пришли PDF, которые нужно добавить в конец, — по одному. Когда закончишь, нажми «Готово»."
	textPDFMergeAdded    = "Добавил. Уже %d %s — пришли ещё или нажми «Готово»." // files so far, plural("файл")
	textPDFMergeNeeded   = "Нужен ещё хотя бы один PDF. Пришли его файлом."
	textPDFNotPDF        = "Это не PDF. Пришли PDF-файл."
	textPDFInvalid       = "Не получилось открыть PDF: файл повреждён или защищён паролем. Пришли другой файл."
	textPDFTooLarge      = "PDF слишком большой: все файлы вместе не должны быть больше %d МБ."
	textPDFTooMany       = "В PDF слишком много страниц: поддерживаю до %d."
	textPDFPrevCancelled = "Предыдущее преобразование отменено."
	textPDFTimeout       = "Обработка заняла слишком много времени. Попробуй файл поменьше."

	// textConvertBestEffort takes the reached size in KB (target was out of reach).
	textConvertBestEffort = "Меньше не вышло — сжал до %d КБ."
)

// Inline button labels for /convert.
const (
	btnPDF         = "📄 Фото → PDF"
	btnCompress    = "🗜 Сжать фото"
	btnPDFActions  = "🛠 Действия с PDF"
	btnDone        = "Готово"
	btnCancel      = "Отмена"
	btnLimit1MB    = "до 1\u00A0МБ"
	btnLimit2MB    = "до 2\u00A0МБ"
	btnLimit5MB    = "до 5\u00A0МБ"
	btnLimitCustom = "Свой лимит"
	btnJPEG        = "JPG"
	btnPNG         = "PNG"
)

// Inline button labels for converting a stored document and for a result.
const (
	btnQuick1MB        = "🗜 до 1\u00A0МБ"
	btnQuick2MB        = "🗜 до 2\u00A0МБ"
	btnQuick5MB        = "🗜 до 5\u00A0МБ"
	btnQuickJPG        = "🖼 В JPG"
	btnQuickPDF        = "📄 В PDF"
	btnQuickCancel     = "Отмена"
	btnPDFRotate       = "🔄 Повернуть"
	btnPDFKeep         = "✂️ Оставить страницы"
	btnPDFDrop         = "➖ Убрать страницы"
	btnPDFMerge        = "➕ Склеить"
	btnSaveResult      = "💾 Сохранить в документы"
	btnSkipResult      = "Не сохранять"
	btnSaveAsNew       = "💾 Сохранить как новый"
	btnReplaceOriginal = "♻️ Заменить оригинал"
	btnReplaceYes      = "Заменить"
	btnReplaceNo       = "Отмена"
)

// Plural forms (one, few, many) for plural().
const (
	pluralPageOne  = "страница"
	pluralPageFew  = "страницы"
	pluralPageMany = "страниц"

	pluralFileOne  = "файл"
	pluralFileFew  = "файла"
	pluralFileMany = "файлов"
)

// Names of files made by the converter: "<name>-<tag>.<ext>".
const (
	nameTagCompressed = "сжатый"
	nameTagPDF        = "PDF"
	nameTagJPG        = "JPG"
	nameTagEdited     = "изменён"

	nameFallbackPhoto    = "фото"
	nameFallbackDocument = "документ"
	nameScan             = "Скан %s.pdf" // %s = date, 02.01.2006
)

// Title suffixes of documents made by the converter.
const (
	suffixCompressed = " (сжатый)"
	suffixJPG        = " (JPG)"
	suffixPDF        = " (PDF)"
	suffixEdited     = " (изменён)"
)
