package telegram

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResultFileName(t *testing.T) {
	tests := []struct {
		name, title, fallback, tag, ext, want string
	}{
		{"extension dropped", "Договор.pdf", nameFallbackDocument, nameTagEdited, "pdf", "Договор-изменён.pdf"},
		{"no extension", "Паспорт", nameFallbackPhoto, nameTagCompressed, "jpg", "Паспорт-сжатый.jpg"},
		{"dotted number kept", "Чек 12.05", nameFallbackPhoto, nameTagPDF, "pdf", "Чек 12.05-PDF.pdf"},
		{"no title: fallback", "", nameFallbackPhoto, nameTagCompressed, "png", "фото-сжатый.png"},
		{"blank title: fallback", "   ", nameFallbackDocument, nameTagEdited, "pdf", "документ-изменён.pdf"},
		{"extension only: fallback", ".jpg", nameFallbackPhoto, nameTagJPG, "jpg", "фото-JPG.jpg"},
		{"path separators replaced", `a/b\c.jpg`, nameFallbackPhoto, nameTagCompressed, "jpg", "a_b_c-сжатый.jpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resultFileName(tt.title, tt.fallback, tt.tag, tt.ext))
		})
	}
}

func TestScanName(t *testing.T) {
	require.Equal(t, "Скан 10.11.2026.pdf", scanName(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)))
}
