package tg

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"
)

// A 403 from Telegram means the user blocked the bot: every send/edit maps it
// to ErrBlocked and logs one Warn line, never Error, never the content.
func TestSend_forbiddenMapsToErrBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	}))
	t.Cleanup(srv.Close)

	calls := map[string]func(*Bot) error{
		"Send":       func(b *Bot) error { return b.Send(context.Background(), 42, "secret text") },
		"SendMarkup": func(b *Bot) error { return b.SendMarkup(context.Background(), 42, "secret text", nil) },
		"SendHTML": func(b *Bot) error {
			_, err := b.SendHTML(context.Background(), 42, "secret text", nil)
			return err
		},
		"EditHTML": func(b *Bot) error { return b.EditHTML(context.Background(), 42, 7, "secret text", nil) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			api, err := bot.New("123:ABC", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
			require.NoError(t, err)
			b := &Bot{api: api, cfg: Config{Token: "123:ABC"}, http: http.DefaultClient, log: log}

			require.ErrorIs(t, call(b), ErrBlocked)

			out := buf.String()
			require.Contains(t, out, `"level":"WARN"`)
			require.NotContains(t, out, `"level":"ERROR"`)
			require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("\n")), "one log line per call")
			require.NotContains(t, out, "secret text")
			require.NotContains(t, out, "123:ABC")
		})
	}
}

func TestSend_otherErrorIsNotBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	}))
	t.Cleanup(srv.Close)
	api, err := bot.New("123:ABC", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	require.NoError(t, err)
	b := &Bot{api: api, http: http.DefaultClient, log: slog.New(slog.DiscardHandler)}

	err = b.Send(context.Background(), 42, "x")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrBlocked)
}
