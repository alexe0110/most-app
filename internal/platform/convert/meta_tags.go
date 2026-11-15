package convert

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bep/imagemeta"
)

// maxListItems caps a list value (a matrix, keywords): the rest is dropped.
const maxListItems = 32

// tagGroup says where a tag shows up and whether it identifies a person, a place
// or a device. Tags it does not know go to GroupOther. Names are the same in
// EXIF, XMP and IPTC where the standards share them.
func tagGroup(name string) (g Group, sensitive bool) {
	if strings.HasPrefix(name, "GPS") {
		return GroupLocation, name != "GPSVersionID" && name != "GPSMapDatum"
	}
	switch name {
	case "Artist", "Author", "Creator", "CameraOwnerName", "OwnerName", "By-line", "Byline", "BylineTitle",
		"Credit", "Writer-Editor", "Copyright", "CopyrightNotice", "Rights":
		return GroupAuthor, true
	case "Make", "Model", "UniqueCameraModel", "HostComputer",
		"SerialNumber", "BodySerialNumber", "CameraSerialNumber", "InternalSerialNumber", "LensSerialNumber":
		return GroupDevice, true
	case "Software", "CreatorTool", "LensMake", "LensModel", "LensInfo", "Lens":
		return GroupDevice, false
	case "DateTime", "DateTimeOriginal", "DateTimeDigitized", "CreateDate", "ModifyDate", "MetadataDate",
		"SubSecTime", "SubSecTimeOriginal", "SubSecTimeDigitized", "OffsetTime", "OffsetTimeOriginal",
		"OffsetTimeDigitized", "DateCreated", "TimeCreated", "DigitalCreationDate", "DigitalCreationTime":
		return GroupDates, false
	case "ExposureTime", "FNumber", "ISO", "ISOSpeedRatings", "PhotographicSensitivity", "FocalLength",
		"FocalLengthIn35mmFilm", "ExposureBiasValue", "Flash", "MeteringMode", "ExposureProgram", "WhiteBalance",
		"ExposureMode", "SceneCaptureType", "BrightnessValue", "ApertureValue", "ShutterSpeedValue",
		"MaxApertureValue", "SensingMethod", "LightSource", "DigitalZoomRatio", "SceneType", "SubjectArea",
		"SubjectDistance", "SubjectDistanceRange", "Contrast", "Saturation", "Sharpness", "GainControl",
		"CustomRendered", "FileSource", "FlashEnergy", "FocalPlaneXResolution", "FocalPlaneYResolution",
		"FocalPlaneResolutionUnit":
		return GroupShooting, false
	case "ImageWidth", "ImageLength", "ImageHeight", "PixelXDimension", "PixelYDimension", "Orientation",
		"XResolution", "YResolution", "ResolutionUnit", "ColorSpace", "ComponentsConfiguration",
		"YCbCrPositioning", "BitsPerSample", "Compression", "ExifVersion", "FlashpixVersion", "SamplesPerPixel",
		"PhotometricInterpretation", "PlanarConfiguration", "YCbCrSubSampling", "WhitePoint",
		"PrimaryChromaticities", "YCbCrCoefficients", "ReferenceBlackWhite", "Gamma", "BitDepth":
		return GroupImage, false
	}
	return GroupOther, false
}

// isThumbnailPointer reports offsets into the file's own bytes: they say nothing
// to a reader.
func isThumbnailPointer(name string) bool {
	switch name {
	case "JPEGInterchangeFormat", "JPEGInterchangeFormatLength", "StripOffsets", "StripByteCounts", "RowsPerStrip":
		return true
	}
	return false
}

// offsetOf names the zone tag that goes with a local EXIF date.
func offsetOf(name string) string {
	switch name {
	case "DateTime", "ModifyDate":
		return "OffsetTime"
	case "DateTimeOriginal":
		return "OffsetTimeOriginal"
	case "DateTimeDigitized", "CreateDate": // the reader names EXIF 0x9004 CreateDate
		return "OffsetTimeDigitized"
	}
	return ""
}

// sourcePrefix names the tag source in a key.
func sourcePrefix(s imagemeta.Source) string {
	switch s {
	case imagemeta.EXIF:
		return "EXIF"
	case imagemeta.IPTC:
		return "IPTC"
	case imagemeta.XMP:
		return "XMP"
	}
	return "Meta"
}

// normalize turns what the tag reader returns into the closed set of Field
// values; nil means "nothing to show".
func normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "undef" {
			return nil
		}
		if !utf8.ValidString(x) { // raw bytes the reader returned as a string
			return Binary{Bytes: len(x)}
		}
		return sanitizeText(x)
	case []string:
		return sanitizeText(strings.Join(x, ", "))
	case bool:
		return x
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(min(x, math.MaxInt64))
	case float32:
		return finite(float64(x))
	case float64:
		return finite(x)
	case []byte:
		return Binary{Bytes: len(x)}
	case time.Time:
		return Moment{Time: x, HasZone: true}
	case []any:
		return normalizeList(x)
	case interface{ Float64() float64 }: // a rational
		return finite(x.Float64())
	}
	return sanitizeText(fmt.Sprint(v))
}

func normalizeList(items []any) any {
	out := make([]any, 0, min(len(items), maxListItems))
	for _, it := range items {
		if len(out) == maxListItems {
			break
		}
		if n := normalize(it); n != nil {
			out = append(out, n)
		}
	}
	switch len(out) {
	case 0:
		return nil
	case 1:
		return out[0]
	}
	return out
}

// finite drops NaN and infinities, which a hostile file can encode.
func finite(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

// exifLayout is how EXIF writes a local date and time.
const exifLayout = "2006:01:02 15:04:05"

// parseMoment reads an EXIF or ISO 8601 date; offset is the "+05:00" of the
// matching OffsetTime tag, if any.
func parseMoment(s, offset string) (Moment, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{time.RFC3339, "2006:01:02 15:04:05Z07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(l, s); err == nil {
			return Moment{Time: t, HasZone: true}, true
		}
	}
	for _, l := range []string{exifLayout, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		t, err := time.Parse(l, s)
		if err != nil {
			continue
		}
		if o, err := time.Parse("-07:00", strings.TrimSpace(offset)); err == nil && offset != "" {
			_, secs := o.Zone()
			loc := time.FixedZone("", secs)
			return Moment{Time: time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), HasZone: true}, true
		}
		return Moment{Time: t}, true
	}
	return Moment{}, false
}

// looksLikeUserPath reports a file path that names a user or a computer: such a
// value leaks who made the file.
func looksLikeUserPath(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "/users/") || strings.Contains(l, `\users\`) || strings.Contains(l, "/home/") ||
		strings.Contains(l, `\documents and settings\`)
}

// addValue puts a tag into the group its name belongs to. A date string becomes a
// Moment (offset is the zone tag that goes with it, "" if none) and a value that
// looks like a user's path is marked sensitive.
func addValue(fs *fieldSet, key, name string, v any, offset string) {
	g, sensitive := tagGroup(name)
	if s, ok := v.(string); ok {
		sensitive = sensitive || looksLikeUserPath(s)
		if g == GroupDates {
			if m, ok := parseMoment(s, offset); ok {
				v = m
			}
		}
	}
	fs.add(g, key, v, sensitive)
}
