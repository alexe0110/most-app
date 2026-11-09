package docsbot

import (
	"strings"
	"unicode"
)

// MaxTitleRunes is the longest title we keep.
const MaxTitleRunes = 100

// NormalizeTitle makes a user-supplied title safe to store and show: control and
// invisible format characters (newlines, tabs, zero-width, bidi marks) become
// spaces, whitespace runs collapse, the result is trimmed and capped at
// MaxTitleRunes runes (a rune is never split). Blank input yields "".
func NormalizeTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || isInvisibleFormat(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if strings.Trim(s, " \u200d") == "" { // only joiners left: visually blank
		return ""
	}
	if r := []rune(s); len(r) > MaxTitleRunes {
		s = strings.TrimSpace(string(r[:MaxTitleRunes]))
	}
	return s
}

// isInvisibleFormat reports zero-width and bidi formatting characters. The
// zero-width joiner (U+200D) is kept: emoji sequences need it.
func isInvisibleFormat(r rune) bool {
	switch {
	case r == 0x200D:
		return false
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E,
		r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069, r == 0xFEFF:
		return true
	}
	return false
}
