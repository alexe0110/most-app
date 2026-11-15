package converter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlural(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n          int
		page, file string
	}{
		{0, "страниц", "файлов"},
		{1, "страница", "файл"},
		{2, "страницы", "файла"},
		{4, "страницы", "файла"},
		{5, "страниц", "файлов"},
		{11, "страниц", "файлов"},
		{12, "страниц", "файлов"},
		{14, "страниц", "файлов"},
		{21, "страница", "файл"},
		{22, "страницы", "файла"},
		{25, "страниц", "файлов"},
		{101, "страница", "файл"},
		{111, "страниц", "файлов"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.n), func(t *testing.T) {
			require.Equal(t, tt.page, plural(tt.n, pluralPageOne, pluralPageFew, pluralPageMany))
			require.Equal(t, tt.file, plural(tt.n, pluralFileOne, pluralFileFew, pluralFileMany))
		})
	}
}

func TestCountTexts_agreeWithTheCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n                   int
		accepted, mergeNote string
	}{
		{1, "Добавил. В PDF уже 1 страница — пришли ещё или нажми «Готово».", "Добавил. Уже 1 файл — пришли ещё или нажми «Готово»."},
		{2, "Добавил. В PDF уже 2 страницы — пришли ещё или нажми «Готово».", "Добавил. Уже 2 файла — пришли ещё или нажми «Готово»."},
		{5, "Добавил. В PDF уже 5 страниц — пришли ещё или нажми «Готово».", "Добавил. Уже 5 файлов — пришли ещё или нажми «Готово»."},
		{11, "Добавил. В PDF уже 11 страниц — пришли ещё или нажми «Готово».", "Добавил. Уже 11 файлов — пришли ещё или нажми «Готово»."},
		{21, "Добавил. В PDF уже 21 страница — пришли ещё или нажми «Готово».", "Добавил. Уже 21 файл — пришли ещё или нажми «Готово»."},
		{22, "Добавил. В PDF уже 22 страницы — пришли ещё или нажми «Готово».", "Добавил. Уже 22 файла — пришли ещё или нажми «Готово»."},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.n), func(t *testing.T) {
			require.Equal(t, tt.accepted, fmt.Sprintf(textConvertPDFAccepted, tt.n, plural(tt.n, pluralPageOne, pluralPageFew, pluralPageMany)))
			require.Equal(t, tt.mergeNote, fmt.Sprintf(textPDFMergeAdded, tt.n, plural(tt.n, pluralFileOne, pluralFileFew, pluralFileMany)))
			require.Equal(t,
				fmt.Sprintf(textResultPages, tt.n, plural(tt.n, pluralPageOne, pluralPageFew, pluralPageMany)),
				fmt.Sprintf(textResultPages, tt.n, plural(tt.n, pluralPageOne, pluralPageFew, pluralPageMany)))
		})
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		n    int64
		want string
	}{
		{"unknown", 0, ""},
		{"negative", -5, ""},
		{"one byte rounds up to a kilobyte", 1, "1 КБ"},
		{"exact kilobyte", 1024, "1 КБ"},
		{"just over a kilobyte", 1025, "2 КБ"},
		{"last kilobyte count", 1<<20 - 1024, "1023\u00A0КБ"},
		{"rounds up into megabytes", 1<<20 - 1, "1,0\u00A0МБ"},
		{"a megabyte", 1 << 20, "1,0 МБ"},
		{"megabytes with a decimal comma", 3*(1<<20) + 1<<19, "3,5 МБ"},
		{"huge", 1 << 40, "1048576,0 МБ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, humanSize(tt.n))
		})
	}
}

func TestCompressedCaption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		before, after int
		want          string
	}{
		{"smaller", 5 << 20, 900 << 10, "Готово: 900 КБ (было 5,0 МБ)."},
		{"megabytes both", 8 << 20, 2 << 20, "Готово: 2,0 МБ (было 8,0 МБ)."},
		{"already small", 100 << 10, 100 << 10, "Готово: 100 КБ (было 100 КБ)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compressedCaption(tt.before, tt.after)
			require.Equal(t, tt.want, got)
			require.False(t, strings.Contains(got, "%!"), got)
		})
	}
}
