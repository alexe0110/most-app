package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestTruncateMiddle_zwjAndCombining(t *testing.T) {
	family := "👨‍👩‍👧" // one glyph, five runes
	tests := []struct {
		name string
		in   string
		n    int
	}{
		{"zwj family", strings.Repeat(family, 6), 12},
		{"flags", strings.Repeat("🇰🇿", 10), 7},
		{"combining accent", strings.Repeat("е́", 20), 9},
		{"huge", strings.Repeat("я", 100000), 30},
		{"empty", "", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMiddle(tt.in, tt.n)
			require.True(t, utf8.ValidString(got), "never cuts inside a rune")
			require.LessOrEqual(t, utf8.RuneCountInString(got), tt.n)
		})
	}
}
