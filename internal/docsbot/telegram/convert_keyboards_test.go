package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot"
)

func TestCallbackDataFitsTelegramLimit(t *testing.T) {
	// A UUID doc id is the longest id we use (36 bytes).
	id := docsbot.DocumentID("123e4567-e89b-12d3-a456-426614174000")
	token := newToken()

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
	for _, m := range saveKeyboard(token, false).InlineKeyboard {
		for _, b := range m {
			data = append(data, b.CallbackData)
		}
	}
	data = append(data, cbAlbumPrefix+token, cbDocPrefix+string(id))

	for _, d := range data {
		require.LessOrEqual(t, len(d), 64, d)
		require.NotEmpty(t, d)
	}
}
