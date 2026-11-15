package converter

import (
	"html"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

var (
	htmlTag   = regexp.MustCompile(`<(/?)([a-z]+)(?:\s[^>]*)?>`)
	htmlEntry = regexp.MustCompile(`&(?:amp|lt|gt|quot|#\d+);`)
)

var telegramTags = []string{"b", "i", "code", "blockquote", "a"}

func assertTelegramHTML(t testing.TB, s string) {
	t.Helper()
	var open []string
	for _, m := range htmlTag.FindAllStringSubmatch(s, -1) {
		name := m[2]
		require.Contains(t, telegramTags, name, "tag %q is not allowed", m[0])
		if m[1] == "/" {
			require.NotEmpty(t, open, "closing %q with nothing open", m[0])
			require.Equal(t, open[len(open)-1], name, "tags are not nested")
			open = open[:len(open)-1]
			continue
		}
		open = append(open, name)
	}
	require.Empty(t, open, "tags left open")
	text := htmlTag.ReplaceAllString(s, "")
	require.NotContains(t, text, "<")
	require.NotContains(t, text, ">")
	require.Equal(t, strings.Count(text, "&"), len(htmlEntry.FindAllString(text, -1)), "every & starts an entity")
}

func telegramLength(s string) int {
	text := html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
	return len(utf16.Encode([]rune(text)))
}

func TestRenderInfo_htmlIsWellFormed(t *testing.T) {
	t.Parallel()
	hostile := convert.FileInfo{Type: convert.FilePNG, Size: 10, Blocks: []convert.MetaBlock{convert.BlockText},
		Groups: []convert.GroupFields{
			group(convert.GroupAuthor, sens("PNG:Author", `<b>A</b> & "q" <a href="x">`)),
			group(convert.GroupOther, fld("PNG:<i>k</i>", "<code>v</code>&amp;")),
		}}
	tests := []struct {
		name  string
		title string
		info  convert.FileInfo
	}{
		{"a photo", "IMG_8092.HEIC", phoneInfo()},
		{"a title with markup", "<i>a</i> & <b>", phoneInfo()},
		{"values with markup", "f", hostile},
		{"a long list", strings.Repeat("Ж<", 3000), convert.FileInfo{Type: convert.FilePDF, Blocks: []convert.MetaBlock{convert.BlockPDFInfo},
			Groups: []convert.GroupFields{group(convert.GroupOther, otherFields(120)...)}}},
		{"encrypted", "a.pdf", convert.FileInfo{Type: convert.FilePDF, Encrypted: true, NeedsPassword: true}},
		{"clean", "a.png", convert.FileInfo{Type: convert.FilePNG, Size: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := renderInfo(tt.title, tt.info)

			assertTelegramHTML(t, got)
			require.LessOrEqual(t, telegramLength(got), maxMessageUnits)
		})
	}
}

func TestRenderInfo_hostileRawKeysAreEscaped(t *testing.T) {
	t.Parallel()
	info := convert.FileInfo{Type: convert.FilePNG, Size: 10, Blocks: []convert.MetaBlock{convert.BlockText},
		Groups: []convert.GroupFields{group(convert.GroupOther, fld("PNG:<i>k</i>", "v"))}}

	got, _ := renderInfo("f", info)

	require.Contains(t, got, "<code>PNG:&lt;i&gt;k&lt;/i&gt;</code>: <code>v</code>")
}
