package telegram

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackFromOrig(t *testing.T) {
	require.Equal(t, "d:lc:auto:2", backFromOrig("l:auto:2"))
	require.Equal(t, "d:sb", backFromOrig("s"))
	require.Empty(t, backFromOrig(""))
}
