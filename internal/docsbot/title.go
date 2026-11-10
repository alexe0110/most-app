package docsbot

import (
	"path/filepath"
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

// DerivedTitle names a document made from src (a compressed copy, a PDF): the
// file extension is dropped and suffix appended. The result fits MaxTitleRunes
// by trimming the base, never the suffix.
func DerivedTitle(src, suffix string) string {
	base := NormalizeTitle(src)
	if ext := filepath.Ext(base); looksLikeExt(strings.TrimPrefix(ext, ".")) {
		base = strings.TrimSpace(strings.TrimSuffix(base, ext))
	}
	suffix = strings.TrimSpace(suffix)
	if base == "" {
		return suffix
	}
	room := MaxTitleRunes - len([]rune(suffix)) - 1
	if r := []rune(base); len(r) > room {
		base = strings.TrimSpace(string(r[:max(room, 0)]))
	}
	return NormalizeTitle(base + " " + suffix)
}

// looksLikeExt reports whether s is plausibly a file extension: 1-5 ASCII
// letters or digits with at least one letter (so a year like "2026" is not one).
func looksLikeExt(s string) bool {
	if len(s) < 1 || len(s) > 5 {
		return false
	}
	letter := false
	for _, r := range s {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !isLetter && (r < '0' || r > '9') {
			return false
		}
		letter = letter || isLetter
	}
	return letter
}

var remoteNameReplacer = strings.NewReplacer("/", "_", "\\", "_")

// mimeExt maps the content types we store to a file extension.
var mimeExt = map[string]string{
	"application/pdf": ".pdf",
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/heic":      ".heic",
}

// RemoteName is the file name a document gets in the cloud: the title without
// path separators plus the extension that matches the content type. A title that
// already ends with that extension is kept; one that ends with a different
// extension-like tail (a JPG title for a PDF) gets the tail replaced. A photo
// without a known content type is a JPEG. An unknown type leaves the title as is.
// Upload and sync both use it, so a file is never renamed back and forth.
func RemoteName(title, mime string, kind Kind) string {
	name := remoteNameReplacer.Replace(title)
	ext := mimeExt[strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))]
	if ext == "" && kind == KindPhoto && mime == "" {
		ext = ".jpg"
	}
	if ext == "" {
		return name
	}
	cur := filepath.Ext(name)
	if looksLikeExt(strings.TrimPrefix(cur, ".")) {
		if sameExt(cur, ext) {
			return name
		}
		name = strings.TrimSuffix(name, cur)
	}
	return name + ext
}

// sameExt compares extensions case-insensitively, treating .jpeg as .jpg.
func sameExt(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(s)
		if s == ".jpeg" {
			return ".jpg"
		}
		return s
	}
	return norm(a) == norm(b)
}
