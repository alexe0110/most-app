package converter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFieldLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		key  string
		want string
	}{
		{"EXIF:Software", "Версия ПО"},
		{"PNG:Software", "Версия ПО"},
		{"EXIF:Make", "Производитель"},
		{"EXIF:Model", "Модель"},
		{"EXIF:LensModel", "Объектив"},
		{"EXIF:DateTimeOriginal", "Снято"},
		{"EXIF:GPSAltitude", "Высота"},
		{"EXIF:GPSImgDirection", "Направление"},
		{"GPS:Coordinates", "Координаты"},
		{"XMP:CreatorTool", "Создано в"},
		{"Image:Size", "Разрешение"},
		{"PDF:Pages", "Страниц"},
		{"PDF:Version", "Версия PDF"},
		{"PDF:IncrementalUpdates", "Правки после создания"},
		{"PDF:Producer", "Конвертер"},
		{"Office:LastModifiedBy", "Последним изменял"},
		{"XMP:DocumentID", ""},
		{"XMP:ContentIdentifier", ""},
		{"PDF:Custom", ""},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, fieldLabel(tt.key))
		})
	}
}
