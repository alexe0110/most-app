package converter

import (
	"testing"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"
	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
	"github.com/alexe0110/most-app/internal/platform/tg"
)

func TestCallbackDataFitsTelegramLimit(t *testing.T) {
	t.Parallel()
	// A UUID doc id is the longest id we use (36 bytes).
	id := docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	token := tg.NewToken()

	var data []string
	for _, m := range quickKeyboard(id, true).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	for _, m := range pdfMenuKeyboard(id).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	for _, m := range resultKeyboard(token, resultCompressed, false).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	data = append(data, convCB(convActAlbum, token), convDocCB(convActDoc, id))

	for _, d := range data {
		require.LessOrEqual(t, len(d), 64, d)
		require.NotEmpty(t, d)
	}
}

func TestResultKeyboard(t *testing.T) {
	t.Parallel()
	token := "abc123"
	tests := []struct {
		name    string
		kind    resultKind
		replace bool
		want    [][]string // label and callback data per row
	}{
		{"compress, not replaceable", resultCompressed, false, [][]string{{btnSaveCompressed, "conv:sv:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
		{"compress, replaceable", resultCompressed, true, [][]string{{btnReplaceCompressed, "conv:rp:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
		{"pdf, not replaceable", resultPDF, false, [][]string{{btnSavePDF, "conv:sv:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
		{"pdf, replaceable", resultPDF, true, [][]string{{btnReplaceOriginal, "conv:rp:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
		{"jpg, not replaceable", resultJPG, false, [][]string{{btnSaveJPG, "conv:sv:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
		{"jpg, replaceable", resultJPG, true, [][]string{{btnReplaceOriginal, "conv:rp:abc123"}, {btnSkipResult, "conv:skip:abc123"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kb := resultKeyboard(token, tt.kind, tt.replace)

			var got [][]string
			for _, row := range kb.InlineKeyboard {
				require.Len(t, row, 1, "one button per row")
				got = append(got, []string{row[0].Text, row[0].CallbackData})
				require.LessOrEqual(t, utf8.RuneCountInString(row[0].Text), 24, row[0].Text)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPDFKeyboards_layout(t *testing.T) {
	t.Parallel()
	id := docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	tests := []struct {
		name string
		kb   func() [][]string // button labels per row
	}{
		{"menu of a stored PDF", func() [][]string { return labels(pdfMenuKeyboard(id)) }},
		{"actions in /convert", func() [][]string { return labels(pdfActionsKeyboard()) }},
	}
	want := [][]string{
		{"🔄 Повернуть", "➕ Склеить"},
		{"✂️ Оставить страницы"},
		{"➖ Убрать страницы"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.kb()
			require.Len(t, got, 4)
			require.Equal(t, want, got[:3])
			require.Len(t, got[3], 1, "cancel is alone in the last row")
			require.Contains(t, got[3][0], "Отмена")
		})
	}
}

func labels(kb *models.InlineKeyboardMarkup) [][]string {
	var rows [][]string
	for _, row := range kb.InlineKeyboard {
		var line []string
		for _, b := range row {
			line = append(line, b.Text)
		}
		rows = append(rows, line)
	}
	return rows
}
