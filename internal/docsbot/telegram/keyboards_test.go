package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCategoryKeyboard_twoPerRowOtherAlone(t *testing.T) {
	kb := categoryKeyboard(actCategory, "d1")
	rows := kb.InlineKeyboard
	require.Len(t, rows, 6)
	for _, row := range rows[:5] {
		require.Len(t, row, 2)
	}
	require.Len(t, rows[5], 1)
	require.Equal(t, "Другое", rows[5][0].Text)
	require.Equal(t, "d:cat:d1:other", rows[5][0].CallbackData)
	require.Equal(t, "Паспорт и ID", rows[0][0].Text)
	require.Equal(t, "Справки", rows[0][1].Text)
}
