package docsbot_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

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
