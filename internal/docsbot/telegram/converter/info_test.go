package converter

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

func TestInspectable(t *testing.T) {
	tests := []struct {
		name string
		doc  docsbot.Document
		want bool
	}{
		{"photo", docsbot.Document{Kind: docsbot.KindPhoto}, true},
		{"heic by mime", docsbot.Document{Kind: docsbot.KindDocument, MIME: "image/heic"}, true},
		{"docx by mime", docsbot.Document{Kind: docsbot.KindDocument, MIME: mimeDOCX}, true},
		{"generic mime, known extension", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/octet-stream", Title: "IMG_1.HEIC"}, true},
		{"generic mime, unknown extension", docsbot.Document{Kind: docsbot.KindDocument, MIME: "application/octet-stream", Title: "a.bin"}, false},
		{"video", docsbot.Document{Kind: docsbot.KindDocument, MIME: "video/mp4"}, false},
		{"over the download limit", docsbot.Document{Kind: docsbot.KindDocument, MIME: mimePDF, FileSize: 21 << 20}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Inspectable(tt.doc))
		})
	}
}

func TestRenderInfo_groupsEscapesAndHidesEmpty(t *testing.T) {
	info := convert.FileInfo{
		Type: convert.FileJPEG, Size: 2 << 20,
		Capture:  convert.Capture{Taken: time.Date(2026, 2, 19, 14, 22, 0, 0, time.UTC), Make: "Apple", Model: "iPhone <15>"},
		Location: &convert.GeoPoint{Lat: 37.5, Lon: -122.25},
		Blocks:   []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP},
	}

	got := renderInfo("a<b>.jpg", info)

	require.Contains(t, got, "a&lt;b&gt;.jpg")
	require.Contains(t, got, "iPhone &lt;15&gt;")
	require.Contains(t, got, "19.02.2026 14:22")
	require.Contains(t, got, textInfoLocationWarn)
	require.Contains(t, got, "https://maps.google.com/?q=37.500000,-122.250000")
	require.NotContains(t, got, groupPeople, "an empty group is left out")
	require.NotContains(t, got, groupPDF)
	require.Contains(t, got, "EXIF, XMP")
	require.NotContains(t, got, textInfoClean)
}

func TestRenderInfo_cleanFile(t *testing.T) {
	got := renderInfo("a.png", convert.FileInfo{Type: convert.FilePNG, Size: 100})

	require.Contains(t, got, textInfoClean)
	require.NotContains(t, got, groupLocation)
}

func TestRenderInfo_cutsLongValues(t *testing.T) {
	info := convert.FileInfo{Type: convert.FileDOCX, People: convert.People{Author: strings.Repeat("я", 500)}}

	got := renderInfo("d", info)

	require.Contains(t, got, strings.Repeat("я", maxShownRunes)+"…")
	require.NotContains(t, got, strings.Repeat("я", maxShownRunes+1))
}

func TestRenderInfo_encryptedPDFIsNotCalledClean(t *testing.T) {
	info := convert.FileInfo{Type: convert.FilePDF, PDF: &convert.PDFFacts{Encrypted: true}}

	got := renderInfo("a.pdf", info)

	require.Contains(t, got, textInfoEncrypted)
	require.NotContains(t, got, textInfoClean)
	require.False(t, canClean(info))
}

func TestInfoKeyboard_cleanButtonOnlyWhenThereIsSomethingToClean(t *testing.T) {
	require.Len(t, infoKeyboard("d1", true).InlineKeyboard[0], 2)
	require.Len(t, infoKeyboard("d1", false).InlineKeyboard[0], 1)
	require.Equal(t, cbMenuCancel, infoKeyboard("d1", false).InlineKeyboard[0][0].CallbackData, "back removes the screen")
}
