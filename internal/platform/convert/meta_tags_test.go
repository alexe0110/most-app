package convert

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTagGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		group     Group
		sensitive bool
	}{
		{"GPSLatitude", GroupLocation, true},
		{"GPSImgDirection", GroupLocation, true},
		{"GPSVersionID", GroupLocation, false},
		{"GPSMapDatum", GroupLocation, false},
		{"Make", GroupDevice, true},
		{"BodySerialNumber", GroupDevice, true},
		{"HostComputer", GroupDevice, true},
		{"Software", GroupDevice, false},
		{"LensModel", GroupDevice, false},
		{"Artist", GroupAuthor, true},
		{"Copyright", GroupAuthor, true},
		{"DateTimeOriginal", GroupDates, false},
		{"OffsetTimeOriginal", GroupDates, false},
		{"ExposureTime", GroupShooting, false},
		{"ISOSpeedRatings", GroupShooting, false},
		{"ImageWidth", GroupImage, false},
		{"Orientation", GroupImage, false},
		{"XMPToolkit", GroupOther, false},
		{"MakerNoteApple", GroupOther, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g, sensitive := tagGroup(tt.name)
			require.Equal(t, tt.group, g)
			require.Equal(t, tt.sensitive, sensitive)
		})
	}
}

func TestLooksLikeUserPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  bool
	}{
		{"/Users/ann/Desktop/contract.pdf", true},
		{`C:\Users\ann\Templates\Normal.dotm`, true},
		{"/home/ann/doc.odt", true},
		{`C:\Documents and Settings\ann\x.doc`, true},
		{"Microsoft Word 2016", false},
		{"users are listed in the report", false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, looksLikeUserPath(tt.value))
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"nothing", nil, nil},
		{"undefined marker", "undef", nil},
		{"empty text", "", ""},
		{"text with a NUL byte", "a\x00b", "ab"},
		{"string list", []string{"a", "b"}, "a, b"},
		{"bytes are binary", []byte{1, 2, 3}, Binary{Bytes: 3}},
		{"small integer", 7, int64(7)},
		{"huge unsigned integer", uint64(math.MaxUint64), int64(math.MaxInt64)},
		{"single precision", float32(1.5), 1.5},
		{"NaN is dropped", math.NaN(), nil},
		{"infinity is dropped", math.Inf(1), nil},
		{"list skips nothing", []any{nil, "x"}, "x"},
		{"empty list", []any{}, nil},
		{"time carries a zone", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Moment{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), HasZone: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, normalize(tt.in))
		})
	}
}

func TestNormalize_listIsCapped(t *testing.T) {
	t.Parallel()
	items := make([]any, 40)
	for i := range items {
		items[i] = "v"
	}
	require.Len(t, normalize(items), maxListItems)
}

func TestParseMoment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		in       string
		offset   string
		want     string // RFC 3339 of the moment
		hasZone  bool
		parsedOK bool
	}{
		{"local time with the zone tag", "2026:02:19 14:22:05", "+05:00", "2026-02-19T14:22:05+05:00", true, true},
		{"local time without a zone tag", "2026:02:19 14:22:05", "", "2026-02-19T14:22:05Z", false, true},
		{"malformed zone tag is ignored", "2026:02:19 14:22:05", "garbage", "2026-02-19T14:22:05Z", false, true},
		{"ISO 8601 with its own zone", "2026-02-19T14:22:05+03:00", "", "2026-02-19T14:22:05+03:00", true, true},
		{"date only", "2026-02-19", "", "2026-02-19T00:00:00Z", false, true},
		{"not a date", "not a date", "", "", false, false},
		{"empty", "", "", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, ok := parseMoment(tt.in, tt.offset)
			require.Equal(t, tt.parsedOK, ok)
			if !ok {
				return
			}
			require.Equal(t, tt.want, m.Time.Format(time.RFC3339))
			require.Equal(t, tt.hasZone, m.HasZone)
		})
	}
}
