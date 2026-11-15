package converter

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

func TestMetadataFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		title string
		want  string
	}{
		{"Скан/паспорта.png", "metadata-Скан_паспорта.txt"},
		{`a\b.pdf`, "metadata-a_b.txt"},
		{"", "metadata-документ.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, metadataFileName(tt.title))
		})
	}
}

// The text file is not HTML: markup in a value stays as typed, and the file holds
// every group in display order, labels where there are labels.
func TestPlainInfo(t *testing.T) {
	t.Parallel()
	info := phoneInfo()
	info.Groups[len(info.Groups)-1].Fields = append(info.Groups[len(info.Groups)-1].Fields, fld("PNG:<b>k</b>", "<i>v</i> & w"))

	got := plainInfo("IMG_8092.HEIC", info)

	require.Contains(t, got, "Координаты: 43.26525, 76.97681\nКарта: https://maps.google.com/?q=43.265250,76.976810\n")
	require.Contains(t, got, "Версия ПО: 26.6\n")
	require.Contains(t, got, "PNG:<b>k</b>: <i>v</i> & w\n", "a text file keeps markup as text")
	require.NotContains(t, got, "&amp;")
	var last int
	for _, title := range []string{"Геолокация", "Устройство", "Даты", "Параметры съёмки", "Изображение", "Прочее"} {
		i := strings.Index(got, "\n"+title+"\n")
		require.Greater(t, i, last, title)
		last = i
	}
}

func TestPlainInfo_keepsEveryFieldWhenTheScreenIsCut(t *testing.T) {
	t.Parallel()
	info := convert.FileInfo{Type: convert.FilePNG, Size: 10, Blocks: []convert.MetaBlock{convert.BlockText},
		Groups: []convert.GroupFields{group(convert.GroupOther, otherFields(100)...)}}

	_, cut := renderInfo("a.png", info)
	got := plainInfo("a.png", info)

	require.True(t, cut, "100 long fields do not fit the screen, so the file is the full list")
	require.Equal(t, 100, strings.Count(got, "XMP:Tag"))
}
