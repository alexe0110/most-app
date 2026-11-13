package docsbot_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestDerivedTitle_edgeCases(t *testing.T) {
	tests := []struct {
		name, src, suffix, want string
	}{
		{"no extension", "Паспорт", " (PDF)", "Паспорт (PDF)"},
		{"dotted date kept", "Скан 01.02.2026", " (PDF)", "Скан 01.02.2026 (PDF)"},
		{"emoji", "Паспорт 🛂", " (сжатый)", "Паспорт 🛂 (сжатый)"},
		{"emoji ZWJ sequence", "👨‍👩‍👧", " (PDF)", "👨‍👩‍👧 (PDF)"},
		{"control chars", "a\nb\t.png", " (PDF)", "a b (PDF)"},
		{"upper-case ext", "IMG.JPEG", " (PDF)", "IMG (PDF)"},
		{"suffix already present", "A (PDF)", " (PDF)", "A (PDF) (PDF)"},
		{"whitespace only", "   ", " (PDF)", "(PDF)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.DerivedTitle(tt.src, tt.suffix))
		})
	}

	for _, src := range []string{
		strings.Repeat("я", 500), strings.Repeat("🛂", 500), strings.Repeat("a", 99) + ".jpg",
		strings.Repeat("x", 95) + ".jpeg", strings.Repeat("👨‍👩", 200),
	} {
		got := docsbot.DerivedTitle(src, " (сжатый)")
		require.LessOrEqual(t, utf8.RuneCountInString(got), docsbot.MaxTitleRunes)
		require.True(t, utf8.ValidString(got))
		require.True(t, strings.HasSuffix(got, "(сжатый)"), "suffix survives trimming")
	}
}

func TestTrimExt_ambiguousTails(t *testing.T) {
	tests := []struct{ in, want string }{
		{"2026.11", "2026.11"},     // digits only: not an extension
		{"Акт 12.05", "Акт 12.05"}, // date-like tail
		{"scan.final", "scan"},     // 5 letters look like an extension (documented heuristic)
		{"a.b.c.pdf", "a.b.c"},     // only the last one goes
		{"Акт.2026а", "Акт.2026а"}, // non-ASCII tail
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.TrimExt(tt.in))
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", " \t\n  ", ""},
		{"plain", "Паспорт", "Паспорт"},
		{"newlines and tabs", "a\nb\t\tc\r\nd", "a b c d"},
		{"control chars", "a\x00b\x07c", "a b c"},
		{"zero-width and bidi", "a\u200bb\u202ec\ufeffd", "a b c d"},
		{"collapse and trim", "  a    b  ", "a b"},
		{"html kept as text", "<script>alert(1)</script>", "<script>alert(1)</script>"},
		{"slash kept", "a/b\\c", "a/b\\c"},
		{"emoji joiner kept", "👨‍👩‍👧", "👨‍👩‍👧"},
		{"10000 chars", strings.Repeat("я", 10000), strings.Repeat("я", docsbot.MaxTitleRunes)},
		{"emoji not split at cap", strings.Repeat("a", 99) + "😀😀", strings.Repeat("a", 99) + "😀"},
		{"trailing space after cap trimmed", strings.Repeat("a", 99) + " b", strings.Repeat("a", 99)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := docsbot.NormalizeTitle(tt.in)
			require.Equal(t, tt.want, got)
			require.True(t, utf8.ValidString(got))
			require.LessOrEqual(t, utf8.RuneCountInString(got), docsbot.MaxTitleRunes)
		})
	}
}

func TestNormalizeTitle_edgeInputs(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"extension only keeps the name", ".pdf", ".pdf"},
		{"hebrew rtl", "  שלום   עולם ", "שלום עולם"},
		{"arabic rtl", "عقد الإيجار", "عقد الإيجار"},
		{"rtl with embedded marks", "abc\u200fdef", "abc def"},
		{"no-break space", "a\u00a0\u00a0b", "a b"},
		{"line and paragraph separators", "a\u2028b\u2029c", "a b c"},
		{"lone zero-width joiner is blank", "\u200d", ""},
		{"zwj with spaces is blank", " \u200d \u200d ", ""},
		{"invalid utf-8 does not panic", "a\xffb", "a�b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.NormalizeTitle(tt.in))
		})
	}
}

func TestRemoteName(t *testing.T) {
	tests := []struct {
		name, title, mime string
		kind              docsbot.Kind
		want              string
	}{
		{"pdf gets extension", "Договор", "application/pdf", docsbot.KindDocument, "Договор.pdf"},
		{"jpeg becomes jpg", "Паспорт", "image/jpeg", docsbot.KindPhoto, "Паспорт.jpg"},
		{"png, webp, heic", "a", "image/png", docsbot.KindDocument, "a.png"},
		{"webp", "a", "image/webp", docsbot.KindDocument, "a.webp"},
		{"heic", "a", "image/heic", docsbot.KindDocument, "a.heic"},
		{"mime parameters ignored", "a", "application/pdf; charset=x", docsbot.KindDocument, "a.pdf"},
		{"photo without mime is jpg", "Фото", "", docsbot.KindPhoto, "Фото.jpg"},
		{"unknown mime keeps title", "a", "application/zip", docsbot.KindDocument, "a"},
		{"document without mime keeps title", "a", "", docsbot.KindDocument, "a"},
		{"same extension is not doubled", "scan.pdf", "application/pdf", docsbot.KindDocument, "scan.pdf"},
		{"same extension, other case", "scan.PDF", "application/pdf", docsbot.KindDocument, "scan.PDF"},
		{"jpeg title for jpg mime", "p.jpeg", "image/jpeg", docsbot.KindPhoto, "p.jpeg"},
		{"different extension is replaced", "scan.JPG", "application/pdf", docsbot.KindDocument, "scan.pdf"},
		{"year is not an extension", "Счёт 01.02.2026", "application/pdf", docsbot.KindDocument, "Счёт 01.02.2026.pdf"},
		{"separators replaced", `a/b\c`, "application/pdf", docsbot.KindDocument, "a_b_c.pdf"},
		{"path traversal", "../etc/passwd", "", docsbot.KindDocument, ".._etc_passwd"},
		{"empty title", "", "", docsbot.KindDocument, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.RemoteName(tt.title, tt.mime, tt.kind))
		})
	}
}

func TestTrimExt(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ВНЖ.pdf", "ВНЖ"},
		{"scan.JPG", "scan"},
		{"Паспорт РК.jpeg", "Паспорт РК"},
		{"archive.tar.gz", "archive.tar"},
		{"Отчёт 2026", "Отчёт 2026"},
		{"v1.2025", "v1.2025"},
		{"Договор", "Договор"},
		{"Справка №1.", "Справка №1."},
		{".pdf", ".pdf"},
		{"", ""},
		{"Длинное.расширение", "Длинное.расширение"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			require.Equal(t, tt.want, docsbot.TrimExt(tt.in))
		})
	}
}
