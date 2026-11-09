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
