package converter

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alexe0110/most-app/internal/platform/convert"
	"github.com/stretchr/testify/require"
)

// otherFields makes n raw tags with long values.

func otherFields(n int) []convert.Field {
	fields := make([]convert.Field, n)
	for i := range fields {
		fields[i] = fld(fmt.Sprintf("XMP:Tag%03d", i), strings.Repeat("я", 150))
	}
	return fields
}

func TestRenderInfo_overflowCutsOtherAndPlainTextKeepsAll(t *testing.T) {
	t.Parallel()
	info := phoneInfo()
	info.Groups[len(info.Groups)-1] = group(convert.GroupOther, otherFields(100)...)

	got, cut := renderInfo("IMG_8092.HEIC", info)

	require.True(t, cut)
	require.LessOrEqual(t, utf8.RuneCountInString(got), maxMessageRunes)
	require.Contains(t, got, "🔧 <b>Прочее</b> · 100\u00a0полей")
	require.Regexp(t, "…и ещё \\d+\u00a0пол", got)
	require.Contains(t, got, "<b>Координаты:</b>", "the groups that matter stay whole")
	require.Contains(t, got, "<b>Снято:</b>")

	plain := plainInfo("IMG_8092.HEIC", info)
	require.Contains(t, plain, "XMP:Tag099: "+strings.Repeat("я", 150))
	require.Contains(t, plain, "Координаты: 43.26525, 76.97681")
	require.Contains(t, plain, "Карта: https://maps.google.com/")
	require.NotContains(t, plain, "<")
}

func TestRenderInfo_worstCaseFitsOneMessage(t *testing.T) {
	t.Parallel()
	groups := []convert.Group{
		convert.GroupLocation, convert.GroupAuthor, convert.GroupDevice, convert.GroupDates, convert.GroupShooting,
		convert.GroupImage, convert.GroupDocument, convert.GroupSecurity, convert.GroupOther,
	}
	info := convert.FileInfo{Type: convert.FilePDF, Size: convert.MaxInputBytes, Blocks: []convert.MetaBlock{convert.BlockPDFInfo}}
	for _, g := range groups {
		info.Groups = append(info.Groups, group(g, otherFields(40)...))
	}

	got, cut := renderInfo(strings.Repeat("Ж<", 3000), info)

	require.True(t, cut)
	require.LessOrEqual(t, utf8.RuneCountInString(got), maxMessageRunes)
	require.LessOrEqual(t, telegramLength(got), maxMessageRunes, "Telegram counts the text without tags, in UTF-16 units")
	require.NotContains(t, got, "Ж<", "the name is escaped")
	assertTelegramHTML(t, got)
}

func TestCleanedCaption(t *testing.T) {
	t.Parallel()
	before := phoneInfo()
	after := convert.FileInfo{Type: convert.FileHEIC, Groups: []convert.GroupFields{
		group(convert.GroupImage, fld("Image:Size", convert.Dimensions{Width: 1, Height: 1}))}}

	tests := []struct {
		name          string
		before, after convert.FileInfo
		want          string
	}{
		{"names the sensitive kinds first", before, after,
			"🧹 Очищенная копия\nУбрал 12\u00a0полей, включая геолокация, направление съёмки, модель устройства"},
		{"nothing sensitive", convert.FileInfo{Groups: []convert.GroupFields{group(convert.GroupDates, fld("EXIF:CreateDate", "x"))}},
			convert.FileInfo{}, "🧹 Очищенная копия\nУбрал 1\u00a0поле"},
		{"nothing counted", convert.FileInfo{}, convert.FileInfo{}, "🧹 Очищенная копия"},
		{"names at most three kinds", convert.FileInfo{Groups: []convert.GroupFields{
			group(convert.GroupLocation, sens("GPS:Coordinates", convert.Coordinates{Lat: 1, Lon: 2})),
			group(convert.GroupAuthor, sens("PDF:Author", "Ann")),
			group(convert.GroupDevice, sens("EXIF:Make", "Apple")),
			group(convert.GroupOther, sens("PDF:Path", "/Users/ann")),
		}}, convert.FileInfo{}, "🧹 Очищенная копия\nУбрал 4\u00a0поля, включая геолокация, автор, модель устройства"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, cleanedCaption(tt.before, tt.after))
		})
	}
}

func TestRenderInfo_hiddenMetadataIsNotCalledClean(t *testing.T) {
	t.Parallel()
	// A text chunk with an empty value: the file has metadata, but no value to show.
	info := convert.FileInfo{Type: convert.FilePNG, Size: 100, Blocks: []convert.MetaBlock{convert.BlockText}}

	got, _ := renderInfo("a.png", info)

	require.NotContains(t, got, textInfoClean, "the file is not clean, so the screen must not say it is")
	require.True(t, canClean(info), "and the copy can be made")
}

// countFieldLines counts the field lines inside the quotes of a rendered screen:

// a labelled one starts with <b>, a raw tag with <code>. Map links and the «…и ещё»

// line are not fields.

func countFieldLines(s string) int {
	n, inQuote := 0, false
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "<blockquote") {
			inQuote = true
		}
		body := blockquoteOpen.ReplaceAllString(line, "")
		if inQuote && (strings.HasPrefix(body, "<b>") || strings.HasPrefix(body, "<code>")) {
			n++
		}
		if strings.Contains(line, "</blockquote>") {
			inQuote = false
		}
	}
	return n
}

var blockquoteOpen = regexp.MustCompile(`^<blockquote[^>]*>`)

// The count in the summary is the number of lines on the screen: a field found

// twice (PDF Info and XMP say the same author) is still a line of its own.

func TestRenderInfo_everyFieldIsOneLine(t *testing.T) {
	t.Parallel()
	pdf, err := convert.Inspect(t.Context(), convertFixture(t, "word.pdf"))
	require.NoError(t, err)
	tests := []struct {
		name string
		info convert.FileInfo
	}{
		{"photo", phoneInfo()},
		{"the same author in PDF Info and XMP", pdf},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, cut := renderInfo("f", tt.info)

			require.False(t, cut)
			require.Equal(t, tt.info.FieldCount(), countFieldLines(got))
		})
	}
}

func TestRenderInfo_onlyShootingImageAndOtherAreFolded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		group  convert.Group
		title  string
		folded bool
	}{
		{convert.GroupLocation, groupLocation, false},
		{convert.GroupAuthor, groupAuthor, false},
		{convert.GroupDevice, groupDevice, false},
		{convert.GroupDates, groupDates, false},
		{convert.GroupShooting, groupShooting, true},
		{convert.GroupImage, groupImage, true},
		{convert.GroupDocument, groupDocument, false},
		{convert.GroupSecurity, groupSecurity, false},
		{convert.GroupOther, groupOther, true},
	}
	for _, tt := range tests {
		t.Run(titleText(tt.title), func(t *testing.T) {
			t.Parallel()
			info := convert.FileInfo{Type: convert.FilePDF, Blocks: []convert.MetaBlock{convert.BlockPDFInfo},
				Groups: []convert.GroupFields{group(tt.group, fld("XMP:Tag", "v"))}}
			tag := "<blockquote>"
			if tt.folded {
				tag = "<blockquote expandable>"
			}

			got, _ := renderInfo("f", info)

			lines := strings.Split(got, "\n")
			i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, boldTitle(tt.title)) })
			require.NotEqual(t, -1, i, "the group has a title")
			require.True(t, i+1 < len(lines) && strings.HasPrefix(lines[i+1], tag), "the quote follows its title")
			require.Equal(t, 1, strings.Count(got, "<blockquote"), "one group, one quote")
		})
	}
}

// telegramTags are the tags Telegram's HTML parse mode accepts.

// assertTelegramHTML checks what Telegram would reject: a tag it does not know,

// a tag left open or closed in the wrong order, and a bare < > or &.

// telegramLength is the length Telegram counts: the text without tags and with

// entities decoded, in UTF-16 units.
