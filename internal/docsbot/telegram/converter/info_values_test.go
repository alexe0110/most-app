package converter

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

func TestFormatValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		key   string
		value any
		want  string
		zone  string
	}{
		{"exposure as a fraction", "EXIF:ExposureTime", 0.008, "1/125\u00a0с", ""},
		{"long exposure", "EXIF:ExposureTime", 2.5, "2,5\u00a0с", ""},
		{"aperture", "EXIF:FNumber", 1.78, "f/1.78", ""},
		{"focal length", "EXIF:FocalLength", 6.765, "6,765\u00a0мм", ""},
		{"direction, north", "EXIF:GPSImgDirection", 359.0, "359° (север)", ""},
		{"direction, east", "EXIF:GPSImgDirection", 90.0, "90° (восток)", ""},
		{"altitude below the sea", "EXIF:GPSAltitude", -12.0, "-12\u00a0м", ""},
		{"orientation", "EXIF:Orientation", int64(6), "поворот на 90° по часовой", ""},
		{"orientation, unknown code", "EXIF:Orientation", int64(42), "42", ""},
		{"flash fired", "EXIF:Flash", int64(9), "сработала", ""},
		{"bias", "EXIF:ExposureBiasValue", 0.5, "+0,5\u00a0EV", ""},
		{"yes", "PDF:JavaScript", true, "да", ""},
		{"no", "PDF:Encrypted", false, "нет", ""},
		{"scan", "PDF:Content", convert.ContentScan, valueScan, ""},
		{"page size", "PDF:PageSize", convert.Dimensions{Width: 210, Height: 297}, "210\u00a0×\u00a0297\u00a0мм", ""},
		{"list", "EXIF:LensInfo", []any{1.5, 6.0}, "1,5, 6", ""},
		{"date with zone", "PDF:CreationDate", convert.Moment{Time: time.Date(2026, 5, 20, 6, 20, 0, 0, time.FixedZone("", -(3*3600+30*60))), HasZone: true},
			"20.05.2026 06:20", "UTC-03:30"},
		{"date without zone", "EXIF:CreateDate", convert.Moment{Time: time.Date(2026, 5, 20, 6, 20, 0, 0, time.UTC)}, "20.05.2026 06:20", ""},
		{"binary", "EXIF:MakerNote", convert.Binary{Bytes: 3}, "двоичные данные, 3\u00a0байта", ""},
		{"resolution unit, inches", "EXIF:ResolutionUnit", int64(2), "дюйм", ""},
		{"resolution unit, centimetres", "EXIF:ResolutionUnit", int64(3), "сантиметр", ""},
		{"orientation, normal", "EXIF:Orientation", int64(1), "обычная", ""},
		{"flash not fired", "EXIF:Flash", int64(0), "не сработала", ""},
		{"metering by pattern", "EXIF:MeteringMode", int64(5), "матричный", ""},
		{"colour space sRGB", "EXIF:ColorSpace", int64(1), "sRGB", ""},
		{"colour space uncalibrated stays a number", "EXIF:ColorSpace", int64(65535), "65535", ""},
		{"pixel width", "EXIF:PixelXDimension", int64(4032), "4032\u00a0пикс.", ""},
		{"focal length in 35 mm", "EXIF:FocalLengthIn35mmFilm", int64(24), "24\u00a0мм", ""},
		{"negative bias", "EXIF:ExposureBiasValue", -0.5, "-0,5\u00a0EV", ""},
		{"coordinates", "GPS:Coordinates", convert.Coordinates{Lat: 43.26525, Lon: 76.97681}, "43.26525, 76.97681", ""},
		{"bidi override is dropped from a value", "EXIF:Software", "26.\u202E6", "26.6", ""},
		{"empty value", "EXIF:Software", "", "", ""},
		{"date without a value", "EXIF:CreateDate", convert.Moment{}, "", ""},
		{"GPS date is written like the other dates", "EXIF:GPSDateStamp", "2026:10:31", "31.10.2026", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, zone := formatValue(tt.key, tt.value)
			require.Equal(t, tt.want, text)
			require.Equal(t, tt.zone, zone)
		})
	}
}
