package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToastFor(t *testing.T) {
	require.Equal(t, toastSending, toastFor([]string{"d", actSend, "1"}))
	require.Equal(t, toastDone, toastFor([]string{"d", actSetCategory, "1", "other"}))
	require.Equal(t, toastDone, toastFor([]string{"d", actDeleteYes, "1"}))
	require.Empty(t, toastFor([]string{"d", actRename, "1"}))
	require.Empty(t, toastFor([]string{"d"}))
}
