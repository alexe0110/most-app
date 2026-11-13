package telegram_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvert_unsupportedDocument(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:compress"))
	h.bot.ProcessUpdate(ctx, documentUpdate("application/zip"))

	require.Empty(t, h.docs)
	require.Contains(t, h.lastText(), "пока не умею")
}

func TestConvert_cancelReturnsToMainScenario(t *testing.T) {
	h := newConvHarness(t)
	ctx := t.Context()

	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "convert"))
	h.bot.ProcessUpdate(ctx, callbackUpdate("conv:cancel"))

	// With the session gone, an unknown command falls through to the catch-all
	// (plain text is now a search, which needs a stored user).
	h.bot.ProcessUpdate(ctx, commandUpdate(testChat, "foobar"))
	require.Contains(t, h.lastText(), "Не понимаю")
}
