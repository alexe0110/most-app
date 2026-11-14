package tg

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/stretchr/testify/require"
)

const testToken = "123:ABC"

// downloadBot is a Bot talking to a fake Bot API: getFile answers the metadata
// request and file serves the bytes.
func downloadBot(t *testing.T, getFile, file http.HandlerFunc) *Bot {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/file/") {
			file(w, r)
			return
		}
		getFile(w, r)
	}))
	t.Cleanup(ts.Close)

	api, err := bot.New(testToken, bot.WithServerURL(ts.URL), bot.WithSkipGetMe())
	require.NoError(t, err)
	return &Bot{
		api:  api,
		cfg:  Config{Token: testToken},
		http: &http.Client{Timeout: 5 * time.Second},
		log:  slog.New(slog.DiscardHandler),
	}
}

// fileInfo answers getFile with a file of the declared size.
func fileInfo(declared int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"file_id":"f","file_path":"secret-path.jpg","file_size":`+strconv.Itoa(declared)+`}}`)
	}
}

// fileBody serves n bytes of image/jpeg.
func fileBody(n int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(make([]byte, n))
	}
}

func TestBot_Download_sizeLimit(t *testing.T) {
	tests := []struct {
		name     string
		declared int // file_size reported by getFile
		served   int // bytes the file endpoint actually sends
		wantErr  error
	}{
		{"exactly the limit", MaxFileBytes, MaxFileBytes, nil},
		{"declared above the limit", MaxFileBytes + 1, MaxFileBytes + 1, ErrFileTooLarge},
		{"undeclared size above the limit", 0, MaxFileBytes + 1, ErrFileTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := downloadBot(t, fileInfo(tt.declared), fileBody(tt.served))

			data, mime, err := b.Download(t.Context(), "f")

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, data, tt.served)
			require.Equal(t, "image/jpeg", mime)
		})
	}
}

func TestBot_Download_getFileTooBig(t *testing.T) {
	getFile := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: file is too big"}`)
	}
	b := downloadBot(t, getFile, fileBody(1))

	_, _, err := b.Download(t.Context(), "f")

	require.ErrorIs(t, err, ErrFileTooLarge)
}

// A failed download must not carry the download link, which holds the bot token.
func TestBot_Download_errorsDoNotLeakToken(t *testing.T) {
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
			b := downloadBot(t, fileInfo(1), tt.file)

			_, _, err := b.Download(t.Context(), "f")

			require.Error(t, err)
			require.NotContains(t, err.Error(), testToken)
		})
	}
}
