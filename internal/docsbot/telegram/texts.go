package telegram

// User-facing texts. Russian, kept in one place per project convention. Tone
// (product.md): documents are "хранятся в Telegram" / "ещё не на Google Диске", the
// goal is the user's Google Drive; never "временно".

// Core flow texts.
const (
	// textStart is a format string: %s = Drive root folder name.
	textStart = "Привет! Я Most Files — храню документы и быстро нахожу нужный.\n\n" +
		"Пришли фото, PDF или файл: паспорт, полис, справку. Выбери категорию — и готово. " +
		"Потом напиши слово из названия, и я пришлю документ.\n\n" +
		"Документы хранятся в Telegram. Хочешь копию на своём Google Диске — подключи его в /settings: " +
		"я создам там папку «%s» и буду видеть только то, что положу в неё сам."

	// textHelp* are joined by helpText: the inline line needs the bot username.
	textHelp = "Что я умею:\n" +
		"• Сохраняю документы — пришли фото, PDF или файл и выбери категорию.\n" +
		"• Нахожу — напиши слово из названия, например «полис».\n" +
		"• /list — все документы по категориям.\n" +
		"• /convert — фото в PDF, сжатие, страницы и склейка PDF.\n"
	textHelpInline = "• В любом чате набери @%s и название — отправлю документ прямо туда.\n" // %s = bot username
	textHelpTail   = "• /settings — Google Диск, отправка из чатов, удаление данных.\n\n" +
		"Документы хранятся в Telegram, а после подключения Google Диска — ещё и на твоём Google Диске."

	// textStartInline answers ?start=inline when inline mode is already on: %s = bot username.
	textStartInline = "Набери @%s и слово из названия в любом чате — я покажу документы, выбери нужный, и он отправится в этот чат."
	// textPrivacy closes /help and /settings: %s = URL of the privacy page.
	textPrivacy = "Как я обращаюсь с данными: %s"

	textDriveConnected = "Google Диск подключён ✅"
	// textConnectIntro is the screen before Google's consent page: %s = Drive root folder name.
	textConnectIntro = "Сейчас откроется Google. Я попрошу доступ только к своей папке «%s» — " +
		"другие файлы на твоём Google Диске я не вижу. " +
		"Если у тебя уже есть папка с таким названием, появится вторая — это нормально."
	textConnectLater = "Хорошо, подключить можно позже в /settings."

	// textFirstDoc follows the first saved document.
	textFirstDoc = "Сохранил. Чтобы найти его потом, напиши слово из названия — или открой /list."

	textChooseCategory      = "В какую категорию сохранить?"
	textChooseCategoryGroup = "Выбери категорию для %d %s:" // files in the album, plural("файл")
	textChooseCategoryAlbum = "Выбери категорию для альбома:"
	textAlbumChoice         = "Альбом: %d %s. Что сделать?"                       // count, plural("изображение")
	textAlbumDuplicates     = "Уже есть у тебя: %d"                               // %d = duplicate files
	textGroupSaved          = "Сохранил %d %s в «%s»."                            // count, plural("документ"), category
	textGroupTooLarge       = "Больше %d\u00A0МБ, остались только в Telegram: %d" // docsbot.MaxDriveBytes in MB, files
	textAlreadySaved        = "Этот документ у тебя уже есть:"
	textReplacedDone        = "♻️ Оригинал заменён" // caption of the card after the converter replaced the file

	textRenamePrompt = "Сейчас: «%s». Пришли новое название — до %d символов." // title (HTML-escaped), docsbot.MaxTitleRunes
	// textChangeCategory replaces the card while the category picker is open: %s = title.
	textChangeCategory = "Новая категория для «%s»:"
	textRenameEmpty    = "Название пустое. Пришли новое название или нажми «Отмена»."
	textRenameTooLong  = "Название слишком длинное, максимум %d символов. Пришли покороче или нажми «Отмена»." // docsbot.MaxTitleRunes

	textDeleteConfirm = "Удалить «%s»? Вернуть его будет нельзя." // %s = title
	// textDeleteConfirmDrive is shown for a document in a connected Drive.
	textDeleteConfirmDrive = "Удалить «%s»? Файл на Google Диске попадёт в корзину — оттуда его можно восстановить в течение 30 дней." // %s = title
	// textDeletedRemoteKept: the Drive file could not be trashed and stays.
	textDeletedRemoteKept = "Документ удалён. На Google Диске файл остался — удали его вручную."
	textDeleted           = "Документ удалён 🗑"
	textGone              = "Этого документа уже нет."

	// Callback toasts.
	toastSending = "Отправляю…"
	toastDone    = "Готово"
	// toastNotRetryable: the upload is no longer failed (double tap or stale button).
	toastNotRetryable = "Уже в работе или готово"

	textCatalogEmpty = "Пока пусто — пришли документ, и он появится здесь."
	textNothingFound = "По запросу «%s» ничего нет. Я ищу по названиям документов — попробуй другое слово или открой /list." // %s = query
	textSearchHint   = "Напиши, что искать: часть названия документа."

	textOfferDrive = "Хочешь, чтобы документы лежали на твоём Google Диске? Подключи — " +
		"новые файлы будут попадать туда автоматически, а бот увидит только то, что сам создал."

	textTransferOffer     = "Загрузить на Google Диск %d %s из Telegram?" // count, plural("документ")
	textNothingToTransfer = "Всё уже на Google Диске."
	textTransferDeclined  = "Хорошо. Загрузить можно позже — в /list или /settings."
	textBackedUp          = "☁️ «%s» теперь на Google Диске."                                // %s = title
	textBackupFailed      = "Не удалось загрузить «%s» на Google Диск. Попробовать ещё раз?" // %s = title
	textReconnect         = "Я потерял доступ к твоему Google Диску — возможно, его отключили в настройках Google. " +
		"Документы в Telegram на месте. Подключи Google Диск заново — кнопкой ниже или в /settings."
	textRetryQueued = "Повторяю загрузку на Google Диск."
	textStatusMore  = "и ещё %d %s" // hidden documents, plural("документ")

	textStoragePending      = "⏳ Загружаю на Google Диск…"
	textStorageInDrive      = "☁️ Google Диск · копия в Telegram"
	textStorageFailed       = "⚠️ Не удалось загрузить на Google Диск"
	textStorageTooLarge     = "📥 Только в Telegram: больше %d\u00A0МБ" // docsbot.MaxDriveBytes in MB
	textStorageSyncing      = "⏳ Обновляю на Google Диске…"
	textStorageSyncFailed   = "⚠️ Не удалось обновить на Google Диске"
	textStorageSyncRevoked  = "⚠️ Не удалось обновить на Google Диске: нет доступа"
	textStorageMissing      = "📥 На Google Диске файла нет, копия осталась в Telegram"
	textDriveFileStayed     = "На Google Диске файл остался в прежней папке — перенеси его вручную, если нужно."
	textStorageTelegramOnly = "📥 Только в Telegram"

	// textGroupNotice is sent once to a group the bot was added to, right before it leaves.
	textGroupNotice = "Я работаю только в личных сообщениях: документы не должны попадать в общий чат. Напиши мне в личку."
	textUnsupported = "Я принимаю фото и файлы. Видео или голосовое пришли как файл: 📎 → Файл."
	// Intake limits: a refused file is not stored.
	textTooManyPending = "Этот файл я не сохранил: сначала выбери категорию для файлов выше. Потом пришли его ещё раз."
	textRateLimited    = "Часть файлов я не сохранил — слишком много сразу. Подожди минуту и пришли их ещё раз."
	textQuotaExceeded  = "У тебя уже %d документов — это максимум. Удали ненужные в /list, и можно будет добавить новые." // %d = docsbot.MaxDocumentsPerUser
	textUnknown        = "Такой команды нет. Вот что есть: /list, /convert, /settings, /help."
	textError          = "Что-то пошло не так. Попробуй ещё раз через минуту — твои документы на месте."
	textSendFailed     = "Не удалось отправить файл. Попробуй ещё раз."
)

// Plural forms (one, few, many) for plural().
const (
	pluralDocumentOne  = "документ"
	pluralDocumentFew  = "документа"
	pluralDocumentMany = "документов"

	pluralFileOne  = "файл"
	pluralFileFew  = "файла"
	pluralFileMany = "файлов"

	pluralImageOne  = "изображение"
	pluralImageFew  = "изображения"
	pluralImageMany = "изображений"
)

// Document card facts, catalog and search headings.
const (
	textFactPages    = "%d\u00A0стр."
	textFactPhoto    = "Фото"
	textFactCategory = "🗂 %s"          // category and the file facts, already joined
	textFactSaved    = "📅 Сохранён %s" // date
	textSizeKB       = "%d\u00A0КБ"
	textSizeMBSuffix = "\u00A0МБ"
	textCatalogTitle = "Твои документы:"
	// textCategoryTitle heads a category page: label, count, plural("документ").
	textCategoryTitle = "%s · %d %s"
	textFoundTitle    = "Нашёл %d %s:" // count, plural("документ")
)

// Command menu descriptions (setMyCommands).
const (
	cmdDescStart    = "Как начать"
	cmdDescList     = "Мои документы по категориям"
	cmdDescConvert  = "Фото в PDF, сжатие, работа с PDF"
	cmdDescSettings = "Google Диск и мои данные"
	cmdDescHelp     = "Что умеет бот"
)

// Settings, disconnect, export and delete texts.
const (
	textDisconnected = "Google Диск отключён. Файлы, которые уже там, остались на месте. " +
		"Новые документы будут храниться в Telegram. Подключить снова можно в /settings."
	textNoFailed = "Документов с ошибкой загрузки нет."

	textExportEmpty     = "Пока нечего присылать — сохранённых документов нет."
	textExportRunning   = "Присылаю документы — подожди, пока закончу."
	textExportStart     = "Присылаю твои документы — это может занять пару минут."
	textExportDone      = "Готово — прислал %d %s."                                              // count, plural("документ")
	textExportTruncated = "Прислал %d из %d. Отправь /export ещё раз, чтобы получить остальные." // sent, total

	textDeleteDataPrompt = "Это удалит все твои данные у бота: документы, категории и подключение к Google Диску.\n\n" +
		"Файлы, уже загруженные на твой Google Диск, останутся у тебя. Сообщения в этом чате бот удалить не может.\n\n" +
		"Советую сначала забрать документы: кнопка ниже или команда /export.\n\n" +
		"Чтобы подтвердить, пришли слово %s. Любое другое сообщение отменит удаление."
	textDataDeleted = "Данные удалены. Файлы на Google Диске остались у тебя; сообщения в этом чате можно удалить вручную.\n\n" +
		"Напиши /start, чтобы начать заново."
	textDeleteWrongWord   = "Удаление отменено, твои данные на месте."
	textDeleteExpired     = "Время на подтверждение вышло — открой /settings ещё раз."
	textDisconnectConfirm = "Отключить Google Диск? Файлы, которые уже там, останутся. " +
		"Новые документы будут храниться в Telegram."
)

// Inline button labels for the core flow.
const (
	btnConnectDrive       = "☁️ Подключить Google Диск"
	btnCardSend           = "📤 Прислать"
	btnCardOpen           = "☁️ Открыть"
	btnOpenDrive          = "☁️ Открыть на Google Диске"
	btnOpenFolder         = "📁 Папка на Google Диске"
	btnOpenCategoryFolder = "📁 Папка категории"
	btnRename             = "✏️ Название"
	btnChangeCat          = "🗂 Категория"
	btnDelete             = "🗑 Удалить"
	btnDeleteYes          = "Да, удалить"
	btnCancel             = "Отмена"
	btnTransfer           = "☁️ Загрузить на Google Диск"
	btnBack               = "⬅️ Назад"
	btnMyDocs             = "📂 Мои документы"
	btnBackToCats         = "⬅️ Категории"
	btnTransferYes        = "Да, загрузить"
	btnTransferNo         = "Не сейчас"
	btnRetryBackup        = "🔁 Повторить"
	btnReupload           = "☁️ Загрузить заново"
	btnReconnect          = "☁️ Подключить заново"
	btnDisconnect         = "🔌 Отключить Google Диск"
	btnDisconnectYes      = "Да, отключить"
	btnContinue           = "☁️ Продолжить"
	btnRetryAll           = "🔁 Повторить загрузку"
	btnExportAll          = "📨 Прислать все документы"
	btnDeleteData         = "🗑 Удалить мои данные"
	btnInlineOn           = "💬 Из любого чата: вкл"
	btnInlineOff          = "💬 Из любого чата: выкл"
)

// Inline mode: the button on top of the inline results (Telegram allows ~64 chars).
const (
	btnInlineUnknown  = "Открыть Most Files"
	btnInlineEmpty    = "Добавить документ"
	btnInlineDisabled = "Включить в настройках"
)

// Inline button labels that open the converter from the docs flow.
const (
	btnConvertCard      = "🛠 Преобразовать"
	btnConvertPicker    = "🛠 Сжать или в PDF"
	btnConvertPickerPDF = "🛠 Действия с PDF"
)

// Inline button labels for an album of images.
const (
	btnAlbumPDF  = "📄 Собрать в один PDF"
	btnAlbumEach = "💾 Сохранить по одному"
)

// Batch status message and /settings.
const (
	textStatusProgress    = "⏳ Загружаю на Google Диск · %d из %d" // done, total
	textStatusDone        = "✅ Загрузил на Google Диск %d %s"      // backed up, plural("документ")
	textStatusPartial     = "⚠️ Загрузил %d из %d"                 // backed up, total
	textStatusLineDone    = "• ✅ %s"                               // title
	textStatusLinePending = "• ⏳ %s"                               // title
	textStatusLineFailed  = "• ⚠️ %s — %s"                         // title, reason
	textStatusCategory    = "📁 %s · %d"                            // category label (maybe a link), count
	textReasonQuota       = "нет места на Google Диске"
	textReasonRevoked     = "доступ отозван"
	textReasonUnavailable = "Google Диск не отвечает"
	textReasonTooLarge    = "больше %d\u00A0МБ"
	// textStatusQuotaHint follows the header when an upload failed for lack of space: retrying cannot help until space is freed.
	textStatusQuotaHint = "Освободи место на Google Диске и нажми «Повторить»."

	textNotInDrive = "📥 %d %s ещё не на Google Диске" // count, plural("документ")
	textSettingsOn = "Google Диск: подключён ✅"
	// textSettingsOff is a format string: %s = Drive root folder name.
	textSettingsOff = "Google Диск: не подключён\n" +
		"Подключи — документы будут копироваться в папку «%s» на твоём Google Диске. Я буду видеть только эту папку."
	textSettingsInline   = "💬 Отправка из любого чата: набери @%s в любом чате." // %s = bot username
	textSettingsTotal    = "Документов: %d"                                      // %d = all documents
	textSettingsInDrive  = "☁️ На Google Диске: %d"                              // %d = backed up
	textSettingsTelegram = "📥 Только в Telegram: %d"                             // %d = documents
	textSettingsFailed   = "⚠️ Не загрузились: %d"                               // %d = documents
)
