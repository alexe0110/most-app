package converter_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/telegram/converter"
)

// The sessions and result tokens are empty after a restart. These tests press old
// buttons against empty stores.

func TestRestart_convertCallbackWithoutSession(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"conv:pdf", "conv:compress", "conv:done", "conv:cancel"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)

			h.bot.ProcessUpdate(t.Context(), callbackUpdate(data))

			require.Equal(t, converter.TextStale, h.lastText())
		})
	}
}

// The cleaned copy waits in memory for the choice. After a restart its buttons
// are old, and the info screen's buttons point at documents that may be gone.
func TestRestart_infoCallbacksWithoutState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		data string
		want string
	}{
		{"conv:kc:x", "Эта кнопка уже не работает. Преобразуй файл ещё раз."},
		{"conv:ko:x", "Эта кнопка уже не работает. Преобразуй файл ещё раз."},
		{"conv:kc:", "Эта кнопка уже не работает. Преобразуй файл ещё раз."},
		{"conv:i:0b9f0c6e-4c1f-4f0e-9d3a-6c1d2f5a7b11", "Этого документа уже нет."},
		{"conv:cl:0b9f0c6e-4c1f-4f0e-9d3a-6c1d2f5a7b11", "Этого документа уже нет."},
	}
	for _, tt := range tests {
		t.Run(tt.data, func(t *testing.T) {
			h := newConvHarness(t)

			h.bot.ProcessUpdate(infoCtx(t), callbackOn(tt.data, 5))

			require.Equal(t, tt.want, h.lastText())
			require.Empty(t, h.docs)
			require.Empty(t, h.downloads)
			require.Empty(t, h.repo.Events)
		})
	}
}
