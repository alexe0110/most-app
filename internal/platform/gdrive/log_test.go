package gdrive

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func debugLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func requireDriveLogClean(t *testing.T, out string) {
	t.Helper()
	for _, leak := range []string{"Паспорт", "%D0", "scan.pdf", "pdf-bytes", "SECRETBODY", "q=", "Authorization", "Bearer"} {
		require.NotContains(t, out, leak)
	}
}

func TestUpload_logsDriveAPIWithoutLeaks(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // connection refused; the *url.Error carries the full URL
		var buf bytes.Buffer
		svc := New(fakeClients{}, newFakeCache(), WithBaseURL(url), WithLogger(debugLog(&buf)))

		_, err := svc.Upload(context.Background(), "user-1", testUpload())

		require.Error(t, err)
		require.NotContains(t, err.Error(), "q=")
		require.NotContains(t, err.Error(), url)
		out := buf.String()
		require.Contains(t, out, `"op":"files.list"`)
		require.Contains(t, out, `"duration_ms"`)
		requireDriveLogClean(t, out)
		require.NotContains(t, out, url)
	})

	t.Run("non-2xx with body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("SECRETBODY scan.pdf"))
		}))
		t.Cleanup(srv.Close)
		var buf bytes.Buffer
		svc := New(fakeClients{}, newFakeCache(), WithBaseURL(srv.URL), WithLogger(debugLog(&buf)))

		_, err := svc.Upload(context.Background(), "user-1", testUpload())

		require.Error(t, err)
		out := buf.String()
		require.Contains(t, out, `"http_status":500`)
		requireDriveLogClean(t, out)
	})

	t.Run("revoked", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(srv.Close)
		var buf bytes.Buffer
		svc := New(fakeClients{}, newFakeCache(), WithBaseURL(srv.URL), WithLogger(debugLog(&buf)))

		_, err := svc.Upload(context.Background(), "user-1", testUpload())

		require.ErrorIs(t, err, ErrAccessRevoked)
		require.Contains(t, buf.String(), `"http_status":401`)
		requireDriveLogClean(t, buf.String())
	})

	t.Run("transport error on the file-name lookup", func(t *testing.T) {
		d := &fakeDrive{}
		base := newService(t, d, fakeClients{}, newFakeCache()).baseURL
		// Fail only the request whose query names the file, as uniqueName sends it.
		httpc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(r.URL.RawQuery, "SECRET") {
				return nil, errors.New("connection reset")
			}
			return http.DefaultTransport.RoundTrip(r)
		})}
		var buf bytes.Buffer
		svc := New(clientFunc(func() *http.Client { return httpc }), newFakeCache(),
			WithBaseURL(base), WithLogger(debugLog(&buf)))
		up := testUpload()
		up.Name = "scan-SECRET.pdf"

		_, err := svc.Upload(context.Background(), "user-1", up)

		require.Error(t, err)
		require.NotContains(t, err.Error(), "SECRET")
		require.NotContains(t, err.Error(), "q=")
		require.NotContains(t, buf.String(), "SECRET")
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type clientFunc func() *http.Client

func (f clientFunc) Client(context.Context, string) (*http.Client, error) { return f(), nil }

func TestUpload_storageQuotaIsErrStorageFull(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{"storage quota", http.StatusForbidden, `{"error":{"errors":[{"reason":"storageQuotaExceeded"}]}}`, ErrStorageFull},
		{"reason only in the message does not match", http.StatusForbidden, `{"error":{"message":"storageQuotaExceeded","errors":[{"reason":"rateLimitExceeded"}]}}`, nil},
		{"non-JSON body falls back to substring", http.StatusForbidden, `storageQuotaExceeded`, ErrStorageFull},
		{"rate limit is not storage", http.StatusForbidden, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, nil},
		{"other status with the same body", http.StatusInternalServerError, `storageQuotaExceeded`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)
			svc := New(fakeClients{}, newFakeCache(), WithBaseURL(srv.URL))

			_, err := svc.Upload(context.Background(), "user-1", testUpload())

			require.Error(t, err)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NotErrorIs(t, err, ErrStorageFull)
			}
		})
	}
}
