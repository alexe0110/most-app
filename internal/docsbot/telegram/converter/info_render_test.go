package converter

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/convert"
)

func TestInspectable(t *testing.T) {
	t.Parallel()
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

// convertFixture reads a metadata fixture of platform/convert.

func convertFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../platform/convert/testdata/" + name)
	require.NoError(t, err)
	return data
}

func fld(key string, v any) convert.Field { return convert.Field{Key: key, Value: v} }

func sens(key string, v any) convert.Field {
	return convert.Field{Key: key, Value: v, Sensitive: true}
}

func group(g convert.Group, fields ...convert.Field) convert.GroupFields {
	return convert.GroupFields{Group: g, Fields: fields}
}

// phoneInfo is an iPhone photo with a place, a direction, a device and a long tail.

func phoneInfo() convert.FileInfo {
	return convert.FileInfo{
		Type: convert.FileHEIC, Size: 1700 << 10,
		Blocks: []convert.MetaBlock{convert.BlockEXIF, convert.BlockXMP},
		Groups: []convert.GroupFields{
			group(convert.GroupLocation,
				sens("GPS:Coordinates", convert.Coordinates{Lat: 43.26525, Lon: 76.97681}),
				sens("EXIF:GPSAltitude", 812.4),
				sens("EXIF:GPSImgDirection", 245.0)),
			group(convert.GroupDevice,
				sens("EXIF:Make", "Apple"), sens("EXIF:Model", "iPhone 17"), fld("EXIF:Software", "26.6")),
			group(convert.GroupDates,
				fld("EXIF:DateTimeOriginal", convert.Moment{
					Time: time.Date(2026, 10, 31, 15, 49, 0, 0, time.FixedZone("", 5*3600)), HasZone: true})),
			group(convert.GroupShooting, fld("EXIF:FNumber", 1.78), fld("EXIF:ExposureTime", 1.0/120), fld("EXIF:ISOSpeedRatings", int64(64))),
			group(convert.GroupImage, fld("Image:Size", convert.Dimensions{Width: 4032, Height: 3024})),
			group(convert.GroupOther, fld("XMP:ContentIdentifier", "A1B2"), fld("EXIF:MakerNoteApple", convert.Binary{Bytes: 512})),
		},
	}
}

func TestRenderInfo_structure(t *testing.T) {
	t.Parallel()

	got, cut := renderInfo("IMG_8092.HEIC", phoneInfo())

	require.False(t, cut)
	for _, want := range []string{
		"ℹ️ <b>О файле</b>\n<code>IMG_8092.HEIC</code> · HEIC · 1,7\u00a0МБ",
		"⚠️ <b>Найдено 13\u00a0полей, чувствительных — 5</b>\n<i>Геолокация, направление съёмки, модель устройства</i>",
		"<b>Координаты:</b> <code>43.26525, 76.97681</code>",
		"https://maps.google.com/?q=43.265250,76.976810",
		"<b>Высота:</b> 812\u00a0м",
		"<b>Направление:</b> 245° (юго-запад)",
		"<b>Версия ПО:</b> 26.6",
		"<b>Снято:</b> 31.10.2026 15:49 <i>(UTC+05:00)</i>",
		"<b>Диафрагма:</b> f/1.78",
		"<b>Выдержка:</b> 1/120\u00a0с",
		"<b>Разрешение:</b> 4032\u00a0×\u00a03024",
		"🔧 <b>Прочее</b> · 2\u00a0поля",
		"<code>XMP:ContentIdentifier</code>: <code>A1B2</code>",
		"<code>EXIF:MakerNoteApple</code>: <code>двоичные данные, 512\u00a0байт</code>",
	} {
		require.Contains(t, got, want)
	}
	// Open groups first, the order of the issue, folded groups last.
	var last int
	for _, title := range []string{groupLocation, groupDevice, groupDates, groupShooting, groupImage, groupOther} {
		name := strings.TrimPrefix(title[strings.Index(title, " ")+1:], " ")
		i := strings.Index(got, "<b>"+name+"</b>")
		require.Greater(t, i, last, name)
		last = i
	}
	require.NotContains(t, got, groupAuthor, "an empty group is left out")
	require.NotContains(t, got, groupSecurity)
	require.Equal(t, 3, strings.Count(got, "<blockquote expandable>"), "shooting, image and other are folded")
	require.Equal(t, 3, strings.Count(got, "<blockquote>"), "location, device and dates are open")
	require.NotContains(t, got, textInfoClean)
}

func TestRenderInfo_summary(t *testing.T) {
	t.Parallel()
	safe := convert.FileInfo{Type: convert.FilePDF, Blocks: []convert.MetaBlock{convert.BlockPDFInfo},
		Groups: []convert.GroupFields{group(convert.GroupDocument, fld("PDF:Pages", int64(4)))}}
	users := convert.FileInfo{Type: convert.FileDOCX, Blocks: []convert.MetaBlock{convert.BlockOfficeProps},
		Groups: []convert.GroupFields{
			group(convert.GroupAuthor, sens("Office:Creator", "Ann"), sens("Office:Company", "Acme")),
			group(convert.GroupDocument, sens("Office:Template", `C:\Users\ann\Normal.dotm`))}}
	clean := convert.FileInfo{Type: convert.FilePNG, Size: 100,
		Groups: []convert.GroupFields{group(convert.GroupImage, fld("Image:Size", convert.Dimensions{Width: 1, Height: 1}))}}

	tests := []struct {
		name string
		info convert.FileInfo
		want string
	}{
		{"nothing sensitive", safe, "✅ <b>Найдено 1\u00a0поле, чувствительных нет</b>"},
		{"people and paths, each kind once", users, "Найдено 3\u00a0поля, чувствительных — 3</b>\n<i>Автор, имя пользователя в пути</i>"},
		{"clean file", clean, "✅ <b>" + textInfoClean + "</b>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := renderInfo("f", tt.info)
			require.Contains(t, got, tt.want)
		})
	}
}

func TestRenderInfo_valuesAreText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"html", "<b>x</b> & y", "&lt;b&gt;x&lt;/b&gt; &amp; y"},
		{"quotes", `"><img src=x>`, "&#34;&gt;&lt;img src=x&gt;"},
		{"control and invisible characters", "a\x00b\u202Ec\u200Bd\x1b[0m", "abcd[0m"},
		{"line breaks", "one\r\ntwo\tthree", "one  two three"},
		{"long value", strings.Repeat("я", 300), strings.Repeat("я", maxShownRunes) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := convert.FileInfo{Type: convert.FileJPEG, Blocks: []convert.MetaBlock{convert.BlockEXIF},
				Groups: []convert.GroupFields{group(convert.GroupDevice, fld("EXIF:Software", tt.value))}}

			got, _ := renderInfo("f", info)

			require.Contains(t, got, "<b>Версия ПО:</b> "+tt.want+"</blockquote>")
		})
	}
}

func TestRenderInfo_cleanFile(t *testing.T) {
	t.Parallel()
	got, cut := renderInfo("a.png", convert.FileInfo{Type: convert.FilePNG, Size: 100})

	require.False(t, cut)
	require.Contains(t, got, textInfoClean)
	require.NotContains(t, got, groupLocation)
	require.NotContains(t, got, "Найдено")
}

func TestRenderInfo_encryptedPDFIsNotCalledClean(t *testing.T) {
	t.Parallel()
	info := convert.FileInfo{Type: convert.FilePDF, Encrypted: true}

	got, _ := renderInfo("a.pdf", info)

	require.Contains(t, got, textInfoEncrypted)
	require.NotContains(t, got, textInfoClean)
	require.False(t, canClean(info))
}

// An owner password alone does not hide the metadata: the reader gets the author
// and the rest, and the screen must show them.
func TestRenderInfo_ownerPasswordOnlyShowsWhatWasRead(t *testing.T) {
	t.Parallel()
	info := convert.FileInfo{Type: convert.FilePDF, Encrypted: true, Blocks: []convert.MetaBlock{convert.BlockPDFInfo},
		Groups: []convert.GroupFields{
			group(convert.GroupAuthor, sens("PDF:Author", "Ann Author")),
			group(convert.GroupSecurity, fld("PDF:Encrypted", true)),
		}}

	got, _ := renderInfo("a.pdf", info)

	require.Contains(t, got, "<b>Автор:</b> Ann Author")
	require.NotContains(t, got, "метаданные не прочитать", "they were read")
}

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

func TestInfoKeyboard_cleanButtonOnlyWhenThereIsSomethingToClean(t *testing.T) {
	t.Parallel()
	require.Len(t, infoKeyboard("d1", true).InlineKeyboard[0], 2)
	require.Len(t, infoKeyboard("d1", false).InlineKeyboard[0], 1)
	require.Equal(t, cbMenuCancel, infoKeyboard("d1", false).InlineKeyboard[0][0].CallbackData, "back removes the screen")
}

func TestFileFormat_coversEveryType(t *testing.T) {
	t.Parallel()
	for _, ft := range []convert.FileType{
		convert.FileJPEG, convert.FilePNG, convert.FileWebP, convert.FileHEIC,
		convert.FilePDF, convert.FileDOCX, convert.FileXLSX, convert.FilePPTX,
	} {
		require.Equal(t, string(ft), string(fileFormat(ft)), "the event value matches the detected type")
	}
}

func TestNotInspectableText(t *testing.T) {
	t.Parallel()
	big := docsbot.Document{Kind: docsbot.KindDocument, MIME: mimePDF, FileSize: 21 << 20}
	require.Equal(t, infoTooLargeText(), notInspectableText(big), "a stale button on a file over the limit")
	require.Contains(t, infoTooLargeText(), "20\u00a0МБ", "the limit is taken from convert.MaxInputBytes")
	require.Equal(t, textInfoUnsupported, notInspectableText(docsbot.Document{Kind: docsbot.KindDocument, MIME: "video/mp4"}))
}

// wrapped is an error as the handlers pass it on: with context around it.
