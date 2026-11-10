package telegram

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"
)

// A failed download must not carry the download link, which holds the bot token.
func TestDownload_errorsDoNotLeakToken(t *testing.T) {
	tests := []struct {
		name string
		file http.HandlerFunc
	}{
		{"connection dropped", func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}},
		{"bad status", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/file/") {
					tt.file(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"f","file_path":"secret-path.jpg"}}`)
			}))
			t.Cleanup(ts.Close)
			b, err := bot.New("123:ABC", bot.WithServerURL(ts.URL), bot.WithSkipGetMe())
			require.NoError(t, err)
			c := newConverter(slog.New(slog.DiscardHandler), nil, nil, nil, nil)

			_, err = c.download(t.Context(), b, "f")

			require.Error(t, err)
			for _, leak := range []string{"123:ABC", ts.URL, "/file/", "secret-path"} {
				require.NotContains(t, err.Error(), leak)
			}
		})
	}
}
