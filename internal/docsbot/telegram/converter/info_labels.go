package converter

import "strings"

// fieldLabel is the human name of a field by its key ("EXIF:Make", "PDF:Pages");
// "" for a tag the dictionary does not know, which the screen shows by its raw
// name. The dictionary is the user-facing text of the info screen, so it lives
// here and not in platform/convert.
func fieldLabel(key string) string {
	source, name, _ := strings.Cut(key, ":")
	switch source {
	case "PDF":
		return pdfLabel(name)
	case "Office":
		return officeLabel(name)
	case "GPS":
		return "Координаты"
	case "Image":
		return "Разрешение"
	case "JPEG":
		return "Комментарий"
	}
	return tagLabel(name)
}

// tagLabel names the tags EXIF, XMP, IPTC and PNG text share.
func tagLabel(name string) string {
	if l := locationLabel(name); l != "" {
		return l
	}
	if l := deviceLabel(name); l != "" {
		return l
	}
	if l := shootingLabel(name); l != "" {
		return l
	}
	return imageLabel(name)
}

func locationLabel(name string) string {
	switch name {
	case "GPSLatitude":
		return "Широта"
	case "GPSLongitude":
		return "Долгота"
	case "GPSAltitude":
		return "Высота"
	case "GPSImgDirection":
		return "Направление"
	case "GPSDestBearing":
		return "Направление к цели"
	case "GPSSpeed":
		return "Скорость"
	case "GPSTimeStamp":
		return "Время GPS"
	case "GPSDateStamp":
		return "Дата GPS"
	case "GPSVersionID":
		return "Версия GPS"
	case "GPSMapDatum":
		return "Система координат"
	case "GPSProcessingMethod":
		return "Способ определения"
	case "GPSDOP":
		return "Точность (DOP)"
	case "GPSHPositioningError":
		return "Погрешность позиции"
	}
	return ""
}

// deviceLabel names the author, device and date tags.
func deviceLabel(name string) string {
	switch name {
	case "Artist", "Author", "Creator", "By-line", "Byline":
		return "Автор"
	case "BylineTitle":
		return "Должность автора"
	case "CameraOwnerName":
		return "Владелец камеры"
	case "OwnerName":
		return "Владелец"
	case "Credit":
		return "Указание автора"
	case "Writer-Editor":
		return "Редактор текста"
	case "Copyright", "CopyrightNotice", "Rights":
		return "Авторские права"
	case "Make":
		return "Производитель"
	case "Model":
		return "Модель"
	case "UniqueCameraModel":
		return "Модель камеры"
	case "HostComputer":
		return "Компьютер"
	case "SerialNumber", "BodySerialNumber", "CameraSerialNumber", "InternalSerialNumber":
		return "Серийный номер"
	case "LensSerialNumber":
		return "Серийный номер объектива"
	case "Software":
		return "Версия ПО"
	case "CreatorTool":
		return "Создано в"
	case "LensMake":
		return "Производитель объектива"
	case "LensModel", "Lens":
		return "Объектив"
	case "LensInfo":
		return "Параметры объектива"
	}
	return dateLabel(name)
}

func dateLabel(name string) string {
	switch name {
	case "DateTime", "ModifyDate":
		return "Изменён"
	case "DateTimeOriginal":
		return "Снято"
	case "DateTimeDigitized", "DigitalCreationDate":
		return "Оцифровано"
	case "CreateDate", "DateCreated":
		return "Создан"
	case "MetadataDate":
		return "Метаданные изменены"
	case "TimeCreated":
		return "Время создания"
	case "DigitalCreationTime":
		return "Время оцифровки"
	case "SubSecTime", "SubSecTimeOriginal", "SubSecTimeDigitized":
		return "Доли секунды"
	case "OffsetTime", "OffsetTimeOriginal", "OffsetTimeDigitized":
		return "Часовой пояс"
	case "tIME":
		return "Изменён"
	}
	return ""
}

func shootingLabel(name string) string {
	switch name {
	case "ExposureTime":
		return "Выдержка"
	case "FNumber":
		return "Диафрагма"
	case "ISO", "ISOSpeedRatings", "PhotographicSensitivity":
		return "ISO"
	case "FocalLength":
		return "Фокусное расстояние"
	case "FocalLengthIn35mmFilm":
		return "Эквивалент для 35 мм"
	case "ExposureBiasValue":
		return "Экспокоррекция"
	case "Flash":
		return "Вспышка"
	case "FlashEnergy":
		return "Мощность вспышки"
	case "MeteringMode":
		return "Замер экспозиции"
	case "ExposureProgram":
		return "Программа экспозиции"
	case "ExposureMode":
		return "Режим экспозиции"
	case "WhiteBalance":
		return "Баланс белого"
	case "SceneCaptureType":
		return "Тип съёмки"
	case "SceneType":
		return "Тип сцены"
	case "BrightnessValue":
		return "Яркость"
	case "ApertureValue":
		return "Значение диафрагмы"
	case "ShutterSpeedValue":
		return "Значение выдержки"
	case "MaxApertureValue":
		return "Максимальная диафрагма"
	case "SensingMethod":
		return "Тип сенсора"
	case "LightSource":
		return "Источник света"
	case "DigitalZoomRatio":
		return "Цифровой зум"
	case "SubjectArea":
		return "Область объекта"
	case "SubjectDistance":
		return "Расстояние до объекта"
	case "SubjectDistanceRange":
		return "Дальность до объекта"
	case "Contrast":
		return "Контраст"
	case "Saturation":
		return "Насыщенность"
	case "Sharpness":
		return "Резкость"
	case "GainControl":
		return "Усиление"
	case "CustomRendered":
		return "Обработка"
	case "FileSource":
		return "Источник файла"
	case "FocalPlaneXResolution":
		return "Разрешение матрицы по горизонтали"
	case "FocalPlaneYResolution":
		return "Разрешение матрицы по вертикали"
	case "FocalPlaneResolutionUnit":
		return "Единица разрешения матрицы"
	}
	return ""
}

func imageLabel(name string) string {
	switch name {
	case "ImageWidth", "PixelXDimension":
		return "Ширина"
	case "ImageLength", "ImageHeight", "PixelYDimension":
		return "Высота"
	case "Orientation":
		return "Ориентация"
	case "XResolution":
		return "Разрешение по горизонтали"
	case "YResolution":
		return "Разрешение по вертикали"
	case "ResolutionUnit":
		return "Единица разрешения"
	case "ColorSpace":
		return "Цветовое пространство"
	case "ComponentsConfiguration":
		return "Состав компонентов"
	case "YCbCrPositioning":
		return "Позиционирование YCbCr"
	case "BitsPerSample":
		return "Бит на канал"
	case "Compression":
		return "Сжатие"
	case "ExifVersion":
		return "Версия EXIF"
	case "FlashpixVersion":
		return "Версия Flashpix"
	case "SamplesPerPixel":
		return "Каналов на пиксель"
	case "PhotometricInterpretation":
		return "Цветовая модель"
	case "PlanarConfiguration":
		return "Расположение данных"
	case "YCbCrSubSampling":
		return "Субдискретизация YCbCr"
	case "WhitePoint":
		return "Белая точка"
	case "PrimaryChromaticities":
		return "Основные цвета"
	case "YCbCrCoefficients":
		return "Коэффициенты YCbCr"
	case "ReferenceBlackWhite":
		return "Опорные чёрный и белый"
	case "Gamma":
		return "Гамма"
	case "BitDepth":
		return "Глубина цвета"
	}
	return ""
}

func pdfLabel(name string) string {
	switch name {
	case "Author":
		return "Автор"
	case "Title":
		return "Название"
	case "Subject":
		return "Тема"
	case "Keywords":
		return "Ключевые слова"
	case "Creator":
		return "Создатель"
	case "Producer":
		return "Конвертер"
	case "CreationDate":
		return "Создан"
	case "ModDate":
		return "Изменён"
	case "IncrementalUpdates":
		return "Правки после создания"
	case "Pages":
		return "Страниц"
	case "PageSize":
		return "Размер страницы"
	case "Content":
		return "Содержимое"
	case "Annotations":
		return "Аннотации и комментарии"
	case "Version":
		return "Версия PDF"
	case "Encrypted":
		return "Шифрование"
	case "JavaScript":
		return "JavaScript"
	case "EmbeddedFiles":
		return "Вложенные файлы"
	case "Signature":
		return "Цифровая подпись"
	}
	return ""
}

func officeLabel(name string) string {
	switch name {
	case "Creator":
		return "Автор"
	case "LastModifiedBy":
		return "Последним изменял"
	case "Company":
		return "Компания"
	case "Manager":
		return "Руководитель"
	case "Created":
		return "Создан"
	case "Modified":
		return "Изменён"
	case "LastPrinted":
		return "Напечатан"
	case "Title":
		return "Название"
	case "Subject":
		return "Тема"
	case "Keywords":
		return "Ключевые слова"
	case "Description":
		return "Описание"
	case "Category":
		return "Категория"
	case "Revision":
		return "Ревизия"
	case "Application":
		return "Приложение"
	case "AppVersion":
		return "Версия приложения"
	case "Template":
		return "Шаблон"
	case "CustomProperties":
		return "Свои свойства"
	}
	return ""
}
