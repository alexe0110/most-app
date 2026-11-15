package converter

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// Limits of the screen.
const (
	// maxShownRunes cuts one value, «…» included: it is a stranger's text and
	// must not fill the message.
	maxShownRunes = 200
	nbsp          = " "
)

// shownField is a field ready for the screen and the text file.
type shownField struct {
	key   string
	label string // "" for a tag the dictionary does not know
	text  string // plain, not escaped
	zone  string // "UTC+05:00" of a date, "" if the file has none
	mono  bool   // a value to copy: serials, ids, coordinates, raw tags
	link  string // map link of a place
}

// showField formats one field. In «Прочее» the raw tag name stays: that is how
// the user can look it up.
func showField(g convert.Group, f convert.Field) shownField {
	s := shownField{key: f.Key}
	if g != convert.GroupOther {
		s.label = fieldLabel(f.Key)
	}
	s.text, s.zone = formatValue(f.Key, f.Value)
	_, name, _ := strings.Cut(f.Key, ":")
	s.mono = g == convert.GroupOther || isCopyable(name)
	if c, ok := f.Value.(convert.Coordinates); ok {
		s.mono = true
		s.link = fmt.Sprintf("https://maps.google.com/?q=%.6f,%.6f", c.Lat, c.Lon)
	}
	return s
}

// isCopyable reports the tags whose value a person copies: serial numbers and ids.
func isCopyable(name string) bool {
	return strings.HasSuffix(name, "SerialNumber") || strings.HasSuffix(name, "ID") ||
		strings.HasSuffix(name, "Identifier") || strings.HasSuffix(name, "UUID")
}

// formatValue writes a value for reading; zone is the time zone of a date.
func formatValue(key string, v any) (text, zone string) {
	_, name, _ := strings.Cut(key, ":")
	switch x := v.(type) {
	case string:
		return formatString(key, x), ""
	case bool:
		if x {
			return valueYes, ""
		}
		return valueNo, ""
	case int64:
		return formatInt(name, x), ""
	case float64:
		return formatFloat(name, x), ""
	case convert.Moment:
		return formatMoment(x)
	case convert.Coordinates:
		return fmt.Sprintf("%.5f, %.5f", x.Lat, x.Lon), ""
	case convert.Dimensions:
		unit := ""
		if key == "PDF:PageSize" {
			unit = nbsp + "мм"
		}
		return fmt.Sprintf("%d%s×%s%d%s", x.Width, nbsp, nbsp, x.Height, unit), ""
	case convert.Binary:
		return fmt.Sprintf(textInfoBinary, x.Bytes, plural(x.Bytes, pluralByteOne, pluralByteFew, pluralByteMany)), ""
	case []any:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			t, _ := formatValue(key, it)
			parts = append(parts, t)
		}
		return cleanText(strings.Join(parts, ", ")), ""
	}
	return "", ""
}

func formatString(key, s string) string {
	if strings.HasSuffix(key, ":GPSDateStamp") {
		if t, err := time.Parse("2006:01:02", s); err == nil {
			return t.Format("02.01.2006")
		}
	}
	if key == "PDF:Content" {
		if s == convert.ContentScan {
			return valueScan
		}
		return valueText
	}
	return cleanText(s)
}

func formatInt(name string, n int64) string {
	switch name {
	case "Orientation", "Flash", "WhiteBalance", "ExposureMode", "ExposureProgram", "MeteringMode", "ColorSpace", "ResolutionUnit":
		if t := enumText(name, n); t != "" {
			return t
		}
	case "ImageWidth", "ImageLength", "ImageHeight", "PixelXDimension", "PixelYDimension":
		return fmt.Sprintf("%d%sпикс.", n, nbsp)
	case "FocalLength", "FocalLengthIn35mmFilm":
		return fmt.Sprintf("%d%sмм", n, nbsp)
	}
	return strconv.FormatInt(n, 10)
}

func formatFloat(name string, f float64) string {
	switch name {
	case "GPSAltitude":
		return fmt.Sprintf("%.0f%sм", f, nbsp)
	case "GPSImgDirection", "GPSDestBearing":
		return fmt.Sprintf("%.0f° (%s)", f, compass(f))
	case "ExposureTime":
		if f > 0 && f < 1 {
			return fmt.Sprintf("1/%.0f%sс", 1/f, nbsp)
		}
		return number(f) + nbsp + "с"
	case "FNumber":
		return "f/" + strings.ReplaceAll(number(f), ",", ".") // the notation is international
	case "FocalLength", "FocalLengthIn35mmFilm":
		return number(f) + nbsp + "мм"
	case "ExposureBiasValue":
		if f > 0 {
			return "+" + number(f) + nbsp + "EV"
		}
		return number(f) + nbsp + "EV"
	case "Orientation", "Flash", "WhiteBalance", "ExposureMode", "ExposureProgram", "MeteringMode", "ColorSpace", "ResolutionUnit":
		if t := enumText(name, int64(f)); t != "" && f == math.Trunc(f) {
			return t
		}
	}
	return number(f)
}

// number writes a float with up to three decimals and a comma.
func number(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return strings.ReplaceAll(s, ".", ",")
}

// compass names the side of the horizon for a bearing in degrees.
func compass(deg float64) string {
	sides := [...]string{"север", "северо-восток", "восток", "юго-восток", "юг", "юго-запад", "запад", "северо-запад"}
	d := math.Mod(deg, 360)
	if d < 0 {
		d += 360
	}
	return sides[int(math.Round(d/45))%len(sides)]
}

// enumText names the codes of the few EXIF enumerations people ask about; "" for
// a code it does not know, which is then shown as the number.
func enumText(name string, n int64) string {
	switch name {
	case "Orientation":
		return map1(n, "обычная", "", "поворот на 180°", "", "", "поворот на 90° по часовой", "", "поворот на 90° против часовой")
	case "Flash":
		if n&1 == 1 {
			return "сработала"
		}
		return "не сработала"
	case "WhiteBalance":
		return map0(n, "авто", "ручной")
	case "ExposureMode":
		return map0(n, "авто", "ручной", "брекетинг")
	case "ExposureProgram":
		return map0(n, "не задана", "ручная", "обычная", "приоритет диафрагмы", "приоритет выдержки")
	case "MeteringMode":
		return map0(n, "неизвестно", "средний", "центровзвешенный", "точечный", "мультиточечный", "матричный", "частичный")
	case "ColorSpace":
		if n == 1 {
			return "sRGB"
		}
	case "ResolutionUnit":
		return map1(n, "", "дюйм", "сантиметр")
	}
	return ""
}

// map0 picks the name of code n counting from 0; map1 counts from 1.
func map0(n int64, names ...string) string {
	if n < 0 || int(n) >= len(names) {
		return ""
	}
	return names[n]
}

func map1(n int64, names ...string) string { return map0(n-1, names...) }

// formatMoment writes a date and time as the file has them, without a zone
// conversion, and the zone apart.
func formatMoment(m convert.Moment) (text, zone string) {
	if m.Time.IsZero() {
		return "", ""
	}
	text = m.Time.Format("02.01.2006 15:04")
	if !m.HasZone {
		return text, ""
	}
	_, secs := m.Time.Zone()
	sign := '+'
	if secs < 0 {
		sign, secs = '-', -secs
	}
	return text, fmt.Sprintf("UTC%c%02d:%02d", sign, secs/3600, secs%3600/60)
}

// cleanText makes a value from a file safe for a message: no control or
// invisible formatting characters (a right-to-left override could reorder what
// the reader sees), one line, cut to maxShownRunes. It does not trust the
// reader of the file to have done it.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxShownRunes {
		s = string(r[:maxShownRunes-1]) + "…" // the mark counts toward the limit
	}
	return s
}
