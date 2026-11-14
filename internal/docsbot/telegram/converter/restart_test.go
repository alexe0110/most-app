package converter_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The sessions and result tokens are empty after a restart. These tests press old
// buttons against empty stores.

func TestRestart_convertCallbackWithoutSession(t *testing.T) {
	for _, data := range []string{"conv:pdf", "conv:compress", "conv:done", "conv:cancel"} {
		t.Run(data, func(t *testing.T) {
			h := newConvHarness(t)

			h.bot.ProcessUpdate(t.Context(), callbackUpdate(data))

			require.Equal(t, "Кнопка устарела, открой заново.", h.lastText())
		})
	}
}
