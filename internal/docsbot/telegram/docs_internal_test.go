package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToastFor(t *testing.T) {
	data := func(s string) callbackData {
		c, _ := parseCallback(s)
		return c
	}
	require.Equal(t, toastSending, toastFor(data("d:snd:1")))
	require.Equal(t, toastDone, toastFor(data("d:sc:1:other")))
	require.Equal(t, toastDone, toastFor(data("d:dy:1")))
	require.Empty(t, toastFor(data("d:rn:1")))
	require.Empty(t, toastFor(data("d:")))
	require.Empty(t, toastFor(callbackData{}))
}
