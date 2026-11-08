package tg

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"
)

// The SDK puts the bot token into the request URL, and a transport error
// carries that URL; the trace log must never show it.
func TestSend_transportErrorDoesNotLeakToken(t *testing.T) {
	const token = "123:ABC"
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	api, err := bot.New(token, bot.WithServerURL(url), bot.WithSkipGetMe())
	require.NoError(t, err)
	b := &Bot{api: api, cfg: Config{Token: token}, http: http.DefaultClient, log: log}

	require.Error(t, b.Send(context.Background(), 42, "secret message text"))

	out := buf.String()
	require.Contains(t, out, `"op":"sendMessage"`)
	require.NotContains(t, out, token)
	require.NotContains(t, out, "secret message text")
	require.NotContains(t, out, `"chat_id`)
}
