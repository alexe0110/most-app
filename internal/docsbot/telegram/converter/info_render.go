package converter

import (
	"fmt"
	"html"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexe0110/most-app/internal/platform/convert"
)

// maxMessageRunes is Telegram's limit for a message. The HTML markup is counted
// too, which is stricter than Telegram: the text can only come out shorter.
const maxMessageRunes = 4096

// renderInfo is the HTML of the info screen. Every value from the file goes
// through html.EscapeString; the labels and group titles come from texts.go and
// info_labels.go. cut reports that fields did not fit the message limit and were
// replaced by «…и ещё N полей»: the caller sends the whole list as a file.
func renderInfo(title string, info convert.FileInfo) (text string, cut bool) {
	head := infoHeader(title, info)
	// Only the encryption flag was read: a user password hides the rest. With an
	// owner password alone the file reads in full and is shown like any other.
	if info.Encrypted && info.FieldCount() <= 1 {
		return head + "\n\n" + textInfoEncrypted, false
	}
	head += "\n\n" + infoSummary(info)

	shown := make([]int, len(info.Groups))
	for i, g := range info.Groups {
		shown[i] = len(g.Fields)
	}
	render := func() string {
		blocks := []string{strings.TrimRight(head, "\n")}
		for i, g := range info.Groups {
			blocks = append(blocks, groupBlock(g, shown[i]))
		}
		return strings.Join(blocks, "\n\n")
	}
	fits := func() bool { return utf8.RuneCountInString(render()) <= maxMessageRunes }
	if fits() {
		return render(), false
	}

	// Cut the least interesting group first, to the most fields that still fit;
	// when even none of it helps, the next group in cutOrder is cut as well:
	// Прочее, Параметры съёмки, Изображение, Документ, Даты, Устройство, Автор,
	// Геолокация, Безопасность. Each keeps its title and the «…и ещё N» line.
	for _, gi := range cutOrder(info.Groups) {
		lo, hi := 0, shown[gi]-1
		for lo < hi {
			shown[gi] = (lo + hi + 1) / 2
			if fits() {
				lo = shown[gi]
			} else {
				hi = shown[gi] - 1
			}
		}
		shown[gi] = lo
		if fits() {
			break
		}
	}
	return render(), true
}

// maxCleanedKinds is how many kinds of sensitive data the caption names.
const maxCleanedKinds = 3

// cleanedCaption says how many fields were removed and what the sensitive ones
// gave away. The count is what the check of the copy no longer finds.
func cleanedCaption(before, after convert.FileInfo) string {
	removed := before.FieldCount() - after.FieldCount()
	if removed <= 0 {
		return textCleanedCaption
	}
	caption := textCleanedCaption + "\n" + fmt.Sprintf(textCleanedRemoved, removed, plural(removed, pluralFieldOne, pluralFieldFew, pluralFieldMany))
	if kinds := sensitiveKinds(before); len(kinds) > 0 {
		caption += fmt.Sprintf(textCleanedRemovedInclude, strings.Join(kinds[:min(len(kinds), maxCleanedKinds)], ", "))
	}
	return caption
}

// infoHeader is the title with the name, type and size of the file.
func infoHeader(title string, info convert.FileInfo) string {
	return fmt.Sprintf("%s\n<code>%s</code> · %s · %s", boldTitle(textInfoTitle),
		html.EscapeString(cleanText(title)), typeName(info.Type), humanSize(int64(info.Size)))
}

// infoSummary says how many fields were found and which of them are sensitive.
func infoSummary(info convert.FileInfo) string {
	n := info.FieldCount()
	switch {
	case info.Clean():
		return "✅ <b>" + textInfoClean + "</b>"
	case n == 0:
		return "⚠️ <b>" + textInfoNoValues + "</b>" // metadata is there, but none of it can be shown
	}
	noun := plural(n, pluralFieldOne, pluralFieldFew, pluralFieldMany)
	sensitive := info.SensitiveFields()
	if len(sensitive) == 0 {
		return fmt.Sprintf(textInfoFoundSafe, n, noun)
	}
	kinds := sensitiveKinds(info)
	return fmt.Sprintf(textInfoFoundRisky, n, noun, len(sensitive), html.EscapeString(capitalize(strings.Join(kinds, ", "))))
}

// sensitiveKinds names what the sensitive fields give away, once each, in the
// order they appear on the screen.
func sensitiveKinds(info convert.FileInfo) []string {
	var kinds []string
	for _, g := range info.Groups {
		for _, f := range g.Fields {
			if !f.Sensitive {
				continue
			}
			if k := sensitiveKind(g.Group, f.Key); !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
	}
	return kinds
}

func sensitiveKind(g convert.Group, key string) string {
	_, name, _ := strings.Cut(key, ":")
	switch g {
	case convert.GroupLocation:
		if name == "GPSImgDirection" || name == "GPSDestBearing" {
			return sensDirection
		}
		return sensLocation
	case convert.GroupAuthor:
		if strings.Contains(name, "Owner") {
			return sensOwner
		}
		return sensAuthor
	case convert.GroupDevice:
		switch {
		case strings.HasSuffix(name, "SerialNumber"):
			return sensSerial
		case name == "HostComputer":
			return sensComputer
		}
		return sensDevice
	}
	return sensUserPath
}

// cutOrder lists group indexes from the one to shorten first: the long tail of
// raw tags, then the technical groups, and the places and people last.
func cutOrder(groups []convert.GroupFields) []int {
	rank := func(g convert.Group) int {
		order := []convert.Group{
			convert.GroupOther, convert.GroupShooting, convert.GroupImage, convert.GroupDocument,
			convert.GroupDates, convert.GroupDevice, convert.GroupAuthor, convert.GroupLocation, convert.GroupSecurity,
		}
		return slices.Index(order, g)
	}
	idx := make([]int, len(groups))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int { return rank(groups[a].Group) - rank(groups[b].Group) })
	return idx
}

// groupMeta is the title of a group and whether it starts folded: what people
// rarely need stays collapsed, what gives them away is always open.
func groupMeta(g convert.Group) (title string, folded bool) {
	switch g {
	case convert.GroupLocation:
		return groupLocation, false
	case convert.GroupAuthor:
		return groupAuthor, false
	case convert.GroupDevice:
		return groupDevice, false
	case convert.GroupDates:
		return groupDates, false
	case convert.GroupShooting:
		return groupShooting, true
	case convert.GroupImage:
		return groupImage, true
	case convert.GroupDocument:
		return groupDocument, false
	case convert.GroupSecurity:
		return groupSecurity, false
	}
	return groupOther, true
}

// groupBlock is a group's title and quote with its first limit fields; the rest
// is told as a count.
func groupBlock(gf convert.GroupFields, limit int) string {
	title, folded := groupMeta(gf.Group)
	heading := boldTitle(title)
	if gf.Group == convert.GroupOther {
		n := len(gf.Fields)
		heading += fmt.Sprintf(textInfoOtherSum, n, plural(n, pluralFieldOne, pluralFieldFew, pluralFieldMany))
	}

	var lines []string
	for _, f := range gf.Fields[:limit] {
		lines = append(lines, fieldLines(showField(gf.Group, f))...)
	}
	if rest := len(gf.Fields) - limit; rest > 0 {
		lines = append(lines, fmt.Sprintf(textInfoMore, rest, plural(rest, pluralFieldOne, pluralFieldFew, pluralFieldMany)))
	}

	tag := "<blockquote>"
	if folded {
		tag = "<blockquote expandable>"
	}
	return heading + "\n" + tag + strings.Join(lines, "\n") + "</blockquote>"
}

// fieldLines is the HTML of one field: its line and, for a place, the map link.
func fieldLines(s shownField) []string {
	if s.text == "" {
		return nil
	}
	value := html.EscapeString(s.text)
	if s.mono {
		value = "<code>" + value + "</code>"
	}
	if s.zone != "" {
		value += " <i>(" + s.zone + ")</i>"
	}
	var line string
	if s.label == "" {
		line = "<code>" + html.EscapeString(s.key) + "</code>: " + value
	} else {
		line = "<b>" + html.EscapeString(s.label) + ":</b> " + value
	}
	lines := []string{line}
	if s.link != "" {
		lines = append(lines, `<a href="`+html.EscapeString(s.link)+`">`+textInfoMapLink+"</a>")
	}
	return lines
}

// boldTitle makes the words of a title bold and leaves its leading emoji out of
// the bold: "📍 Геолокация" -> "📍 <b>Геолокация</b>".
func boldTitle(s string) string {
	emoji, name, ok := strings.Cut(s, " ")
	if !ok {
		return "<b>" + s + "</b>"
	}
	return emoji + " <b>" + name + "</b>"
}

// titleText is a title without its leading emoji, for plain text.
func titleText(s string) string {
	_, name, _ := strings.Cut(s, " ")
	return name
}

// capitalize upper-cases the first letter.
func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func typeName(t convert.FileType) string {
	switch t {
	case convert.FileJPEG:
		return typeJPEG
	case convert.FilePNG:
		return typePNG
	case convert.FileWebP:
		return typeWebP
	case convert.FileHEIC:
		return typeHEIC
	case convert.FilePDF:
		return typePDF
	case convert.FileDOCX:
		return typeDOCX
	case convert.FileXLSX:
		return typeXLSX
	case convert.FilePPTX:
		return typePPTX
	}
	return ""
}
