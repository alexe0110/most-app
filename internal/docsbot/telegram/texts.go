package telegram

// User-facing texts. Russian, kept in one place per project convention. Tone
// (product.md): documents are "хранятся в Telegram" / "ещё не на Google Диске", the
// goal is the user's Google Drive; never "временно".

// Core flow texts (#9, #10, #12, #13, #14).
const (
	// textStart is a format string: %s = Drive root folder name.
	textStart = "Привет! Я Most Docs. Пришли документ — фото, PDF или файл, — " +
		"я разложу его по категориям и найду по запросу. А ещё сожму фото и поработаю с PDF — команда /convert.\n\n" +
		"Цель — хранить документы на твоём Google Диске. Пока Google Диск не подключён, " +
		"они лежат во входящей папке в Telegram — можно начать без настройки.\n\n" +
		"Подключишь Google Диск — бот будет видеть только файлы, которые сам создал. " +
		"Он создаёт свою папку «%s» на твоём Диске и не видит папки, которые ты создаёшь сам, " +
		"поэтому если у тебя уже есть папка с таким именем — появится вторая."

	textHelp = "Что я умею:\n" +
		"• Приём документов — пришли фото, PDF или файл и выбери категорию.\n" +
		"• /list — твои документы по категориям.\n" +
		"• Поиск — напиши слово из названия.\n" +
		"• /convert — фото → PDF, сжать изображение, действия с PDF: повернуть, оставить или убрать страницы, склеить.\n\n" +
		"Цель — перенести документы на твой Google Диск; пока они хранятся в Telegram."

	textDriveConnected = "Google Диск подключён ✅"

	textChooseCategory      = "Выбери категорию:"
	textChooseCategoryGroup = "Выбери категорию для %d %s:" // files in the album, plural("файл")
	textChooseCategoryAlbum = "Выбери категорию для альбома:"
	textAlbumChoice         = "Альбом: %d %s. Что сделать?"                               // count, plural("изображение")
	textAlbumDuplicates     = "Уже были сохранены: %d"                                    // %d = duplicate files
	textGroupSaved          = "Сохранил %d %s в категории «%s»."                          // count, plural("файл"), category
	textGroupTooLarge       = "Слишком большие для Google Диска, остались в Telegram: %d" // %d = files
	textAlreadySaved        = "Этот файл уже сохранён."

	textRenamePrompt  = "Пришли новое название"
	textRenameEmpty   = "Название пустое. Пришли новое название или нажми «Отмена»."
	textRenameTooLong = "Название слишком длинное, максимум %d символов. Пришли покороче или нажми «Отмена»." // docsbot.MaxTitleRunes

	textDeleteConfirm = "Удалить этот документ? Отменить будет нельзя."
	// textDeleteConfirmDrive is shown for a document in a connected Drive.
	textDeleteConfirmDrive = "Удалить этот документ? Файл будет перемещён в корзину Google Диска."
	// textDeletedRemoteKept: the Drive file could not be trashed and stays.
	textDeletedRemoteKept = "Документ удалён. На Google Диске файл остался — удали его вручную."
	textDeleted           = "Документ удалён 🗑"
	textDeleteCancelled   = "Удаление отменено."
	textGone              = "Этого документа уже нет."
	textStale             = "Кнопка устарела, открой заново."

	// Callback toasts.
	toastSending = "Отправляю…"
	toastDone    = "Готово"
	// toastNotRetryable: the upload is no longer failed (double tap or stale button).
	toastNotRetryable = "Уже в работе или готово"

	textCatalogEmpty = "Пока пусто — пришли документ, и он появится здесь."
	textNothingFound = "Ничего не нашёл. Загляни в /list или пришли новый документ."
	textSearchHint   = "Напиши, что искать: часть названия документа."

	textOfferDrive = "Хочешь, чтобы документы лежали на твоём Google Диске? Подключи — " +
		"новые файлы будут попадать туда автоматически, а бот увидит только то, что сам создал."

	textTransferOffer     = "Перенести %d сохранённых документов на Google Диск?" // %d = count
	textNothingToTransfer = "Всё уже на Google Диске."
	textTransferDeclined  = "Хорошо. Перенести можно позже — в /list или /settings."
	textBackedUp          = "☁️ «%s» теперь на Google Диске." // %s = title
	textBackupFailed      = "Не удалось выгрузить документ на Google Диск. Попробовать ещё раз?"
	textReconnect         = "Доступ к Google Диску отозван. Подключи заново, чтобы продолжить выгрузку."
	textRetryQueued       = "Повторяю выгрузку на Google Диск."
	textStatusMore        = "…и ещё %d" // %d = hidden titles

	textStoragePending      = "⏳ Загружаю на Google Диск…"
	textStorageInDrive      = "☁️ Google Диск · копия в Telegram"
	textStorageFailed       = "❌ Не удалось выгрузить на Google Диск"
	textStorageTooLarge     = "📥 Слишком большой для Google Диска"
	textStorageSyncing      = "⏳ Обновляю на Google Диске…"
	textStorageSyncFailed   = "⚠️ Не удалось обновить в Google Диске"
	textStorageSyncRevoked  = "⚠️ Не удалось обновить в Google Диске: доступ отозван"
	textStorageMissing      = "📥 Файл удалён в Google Диске, копия осталась в Telegram"
	textDriveFileStayed     = "На Google Диске файл остался в прежней папке"
	textStorageTelegramOnly = "📥 Только в Telegram"

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
	textDisconnected = "Google Диск отключён. Новые документы останутся в Telegram, " +
		"а уже выгруженные файлы и ссылки на них сохранены."
	textNoFailed = "Документов с ошибкой выгрузки нет."

	textExportEmpty     = "Пока нечего присылать — сохранённых документов нет."
	textExportRunning   = "Присылаю документы — подожди, пока закончу."
	textExportStart     = "Присылаю твои документы…"
	textExportDone      = "Готово — прислал %d документов."                                      // %d = count
	textExportTruncated = "Прислал %d из %d. Отправь /export ещё раз, чтобы получить остальные." // sent, total

	textDeleteDataPrompt = "Это удалит все твои данные у бота: документы, категории и подключение к Google Диску.\n\n" +
		"Файлы, уже выгруженные на твой Google Диск, останутся у тебя. Сообщения в этом чате бот удалить не может.\n\n" +
		"Советую сначала забрать документы: кнопка ниже или команда /export.\n\n" +
		"Чтобы подтвердить, пришли слово УДАЛИТЬ."
	textDataDeleted = "Данные удалены. Файлы на Google Диске остались у тебя; сообщения в этом чате можно удалить вручную.\n\n" +
		"Напиши /start, чтобы начать заново."
	textDeleteWrongWord = "Удаление отменено — слово не совпало."
)

// Inline button labels for the core flow.
const (
	btnConnectDrive       = "☁️ Подключить Google Диск"
	btnCardSend           = "📤 Прислать"
	btnCardOpen           = "☁️ Открыть"
	btnOpenDrive          = "☁️ Открыть в Google Диске"
	btnOpenFolder         = "📁 Папка в Google Диске"
	btnOpenCategoryFolder = "📁 Папка категории"
	btnRename             = "✏️ Имя"
	btnChangeCat          = "🗂 Категория"
	btnDelete             = "🗑 Удалить"
	btnDeleteYes          = "Да, удалить"
	btnDeleteNo           = "Отмена"
	btnRenameCancel       = "Отмена"
	btnTransfer           = "📤 Перенести на Google Диск"
	btnBack               = "⬅️ Назад"
	btnMyDocs             = "📂 Мои документы"
	btnBackToCats         = "⬅️ Категории"
	btnTransferYes        = "Да, перенести"
	btnTransferNo         = "Не сейчас"
	btnRetryBackup        = "🔁 Повторить"
	btnReupload           = "☁️ Выгрузить заново"
	btnReconnect          = "☁️ Переподключить Google Диск"
	btnDisconnect         = "🔌 Отключить Google Диск"
	btnRetryAll           = "🔁 Повторить выгрузку"
	btnExportAll          = "📨 Сначала прислать все документы"
	btnDeleteData         = "🗑 Удалить мои данные"
	btnInlineOn           = "💬 Отправка из любого чата: вкл"
	btnInlineOff          = "💬 Отправка из любого чата: выкл"
)

// Inline mode: the button on top of the inline results (Telegram allows ~64 chars).
const (
	btnInlineUnknown  = "Открыть Most Files"
	btnInlineEmpty    = "Добавить документ"
	btnInlineDisabled = "Включить в настройках"
)

// Converter mode (/convert) texts.
const (
	textConvertStart = "Что сделать?"

	textConvertPDFPrompt      = "Пришли фото — по одному или альбомом. Когда закончишь, нажми «Готово»."
	textConvertPDFAccepted    = "Принято, страниц: %d. Пришли ещё или нажми «Готово»." // %d = pages so far
	textConvertCompressPrompt = "Пришли одно фото для сжатия."
	textConvertPDFAwait       = "Пришли PDF файлом."
	textConvertPDFMenu        = "Что сделать с PDF?"

	// textConvertFileTip is added once per user, after they send a compressed photo.
	textConvertFileTip = "Совет: отправляй снимки «как файл» (до 20 МБ) — иначе Telegram сильно ужимает качество."

	textConvertChooseLimit  = "До какого размера сжать?"
	textConvertChooseFormat = "В каком формате сохранить?"
	textConvertCustomLimit  = "Введи лимит в килобайтах, например 300."

	textConvertBadNumber = "Нужно целое число килобайт, например 300."
	textConvertTapButton = "Нажми кнопку выше или /cancel, чтобы выйти."
	textConvertNoImages  = "Сначала пришли хотя бы одно фото."

	textConvertUnsupported        = "Так я пока не умею. Пришли фото в JPEG, PNG или WEBP. Видео и другие форматы — позже."
	textConvertDownloadFailed     = "Не удалось получить файл. Попробуй ещё раз."
	textConvertTooLarge           = "Изображение слишком большое. Пришли снимок поменьше."
	textConvertFormatNotSupported = "Этот формат пока не поддерживается. Пришли JPG, PNG или WEBP."
	textConvertFailed             = "Не удалось обработать изображение. Проверь, что это JPEG, PNG или WEBP."

	textConvertCancelled = "Режим конвертера выключен."
	textConvertNothing   = "Сейчас нечего отменять."

	textConvertProgress = "⏳ Обрабатываю…"
	textConvertDone     = "Готово ✅"

	textConvertMenu      = "Что сделать с файлом?"
	textConvertSaving    = "💾 Сохраняю…"
	textConvertResultOld = "Эта кнопка устарела. Преобразуй файл ещё раз."

	textReplaceConfirm = "Исходный файл будет заменён"
	textReplacedDone   = "♻️ Исходный файл заменён"
	textReplaceBusy    = "Подожди, файл ещё сохраняется"
	textReplaceMissing = "Файл удалён в Google Диске: сначала «Выгрузить заново»"

	textAlbumTooMany = "В один PDF помещается не больше %d страниц. Сохрани фото по одному."
	textAlbumTooBig  = "Фото в альбоме больше %d МБ вместе — в один PDF не поместятся. Сохрани их по одному."

	textPDFRangePrompt   = "Какие страницы? Например: 1-3,5"
	textPDFBadRange      = "Не понял, какие страницы. Напиши, например: 1-3,5. Нельзя оставить файл без страниц."
	textPDFMergePrompt   = "Пришли PDF, которые нужно добавить в конец, — по одному. Когда закончишь, нажми «Готово»."
	textPDFMergeAdded    = "Добавил. Всего файлов: %d. Пришли ещё или нажми «Готово»."
	textPDFMergeNeeded   = "Нужен ещё хотя бы один PDF. Пришли его файлом."
	textPDFNotPDF        = "Это не PDF. Пришли PDF-файл."
	textPDFInvalid       = "Не получилось прочитать PDF: файл повреждён или защищён паролем."
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
	btnCompress    = "🗜 Сжать"
	btnPDFActions  = "📑 Действия с PDF"
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
	btnConvertCard      = "🛠 Преобразовать"
	btnConvertPicker    = "🛠 Сжать / В PDF"
	btnConvertPickerPDF = "🛠 Действия с PDF"
	btnQuick1MB         = "🗜 1 МБ"
	btnQuick2MB         = "🗜 2 МБ"
	btnQuick5MB         = "🗜 5 МБ"
	btnQuickJPG         = "🖼 В JPG"
	btnQuickPDF         = "📄 В PDF"
	btnQuickCancel      = "✖️ Отмена"
	btnPDFRotate        = "🔄 Повернуть ↻"
	btnPDFKeep          = "✂️ Оставить стр."
	btnPDFDrop          = "🗑 Убрать стр."
	btnPDFMerge         = "➕ Склеить"
	btnSaveResult       = "💾 Сохранить в документы"
	btnSkipResult       = "⬇️ Только файл"
	btnSaveAsNew        = "💾 Сохранить как новый"
	btnReplaceOriginal  = "♻️ Заменить оригинал"
	btnReplaceYes       = "Заменить"
	btnReplaceNo        = "Отмена"
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

// Inline button labels for an album of images.
const (
	btnAlbumPDF  = "📄 Собрать в один PDF"
	btnAlbumEach = "💾 Сохранить по одному"
)

// Batch status message (#99) and /settings.
const (
	textStatusProgress    = "⏳ Переношу на Google Диск · %d из %d" // done, total
	textStatusDone        = "✅ Перенёс на Google Диск — %d %s"     // backed up, plural("документ")
	textStatusNotDone     = "⚠️ Не перенёс %d: %s"                 // count, reason
	textStatusNotDoneList = "⚠️ Не перенёс %d:"                    // count; reasons follow
	textStatusReasonLine  = "• %s — %d"                            // reason, count
	textStatusCategory    = "📁 %s · %d"                            // category label (maybe a link), count
	textReasonQuota       = "нет места на Диске"
	textReasonRevoked     = "доступ отозван"
	textReasonUnavailable = "сервис недоступен"
	textReasonTooLarge    = "больше %d МБ"

	textNotInDrive       = "📥 %d ещё не на Google Диске" // %d = documents
	textSettingsOn       = "Google Диск: подключён ✅"
	textSettingsOff      = "Google Диск: не подключён"
	textSettingsTotal    = "Документов: %d"          // %d = all documents
	textSettingsInDrive  = "☁️ На Google Диске: %d"  // %d = backed up
	textSettingsTelegram = "📥 Только в Telegram: %d" // %d = documents
	textSettingsFailed   = "⚠️ Не выгрузились: %d"   // %d = documents
)
