package web_test

import (
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexe0110/most-app/internal/docsbot/web"
)

const (
	testEmail = "hello@most.example"

	limitedUse = "Most Files's use and transfer to any other app of information received from Google APIs " +
		"will adhere to the Google API Services User Data Policy, including the Limited Use requirements."
	policyURL = `href="https://developers.google.com/terms/api-services-user-data-policy#additional_requirements_for_specific_api_scopes"`
)

var (
	tagRe    = regexp.MustCompile(`<[^>]+>`)
	dateRe   = regexp.MustCompile(`\d{2}\.\d{2}\.\d{4}`)
	mailtoRe = regexp.MustCompile(`href="mailto:([^"]*)"`)
)

// externalMarkers would make a page load something from another origin or run code.
var externalMarkers = []string{"<script", "<link", "<img", "<iframe", "<object", "<embed", " src=", "@import", "url("}

func newMux(t *testing.T, email string) *http.ServeMux {
	t.Helper()
	pages, err := web.New(web.Config{ContactEmail: email})
	require.NoError(t, err)
	mux := http.NewServeMux()
	pages.RegisterRoutes(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req, err := http.NewRequestWithContext(t.Context(), method, path, nil)
	require.NoError(t, err)
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPages_get(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		contains []string
		plain    []string // checked after stripping tags
	}{
		{
			name:     "home",
			path:     "/",
			contains: []string{"Most Files", `href="/privacy"`, `href="/terms"`, `href="https://t.me/most_files_bot"`},
		},
		{
			name:     "privacy",
			path:     "/privacy",
			contains: []string{"Most Files", "drive.file", policyURL, "[УТОЧНИТЬ]"},
			plain:    []string{limitedUse},
		},
		{
			name:     "terms",
			path:     "/terms",
			contains: []string{"Most Files", `href="/privacy"`, "Republic of Kazakhstan"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, newMux(t, testEmail), http.MethodGet, tt.path)

			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
			require.NotEmpty(t, rec.Header().Get("Cache-Control"))
			require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			require.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'none'")
			require.Equal(t, "13.11.2026", revisionIn(t, rec.Body.String()))

			body := rec.Body.String()
			for _, s := range tt.contains {
				require.Contains(t, body, s)
			}
			plain := html.UnescapeString(tagRe.ReplaceAllString(body, ""))
			for _, s := range tt.plain {
				require.Contains(t, plain, s)
			}
			require.Contains(t, body, `href="mailto:`+testEmail+`"`)
			require.Contains(t, plain, testEmail)

			// Language switch: both anchors and both links to them.
			for _, s := range []string{`id="ru"`, `id="en"`, `href="#ru"`, `href="#en"`} {
				require.Contains(t, body, s)
			}
			for _, s := range externalMarkers {
				require.NotContains(t, strings.ToLower(body), s)
			}
		})
	}
}

func TestPrivacy_limitedUseInBothLanguages(t *testing.T) {
	body := do(t, newMux(t, testEmail), http.MethodGet, "/privacy").Body.String()
	plain := html.UnescapeString(tagRe.ReplaceAllString(body, ""))

	require.Equal(t, 2, strings.Count(plain, limitedUse))
	require.Equal(t, 2, strings.Count(body, policyURL))
}

func TestTemplates_revisionDefinedOnce(t *testing.T) {
	files, err := filepath.Glob("templates/*.html")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	var found []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		found = append(found, dateRe.FindAllString(string(src), -1)...)
	}

	require.Equal(t, []string{"13.11.2026"}, found)
}

func revisionIn(t *testing.T, body string) string {
	t.Helper()
	m := dateRe.FindAllString(body, -1)
	require.NotEmpty(t, m)
	for _, d := range m {
		require.Equal(t, m[0], d, "revision date must be the same everywhere")
	}
	return m[0]
}

func TestPages_head(t *testing.T) {
	// A real server: only it drops the body of a HEAD response.
	srv := httptest.NewServer(newMux(t, testEmail))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/", "/privacy", "/terms"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodHead, srv.URL+path, nil)
			require.NoError(t, err)

			resp, err := srv.Client().Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Empty(t, body)
			require.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
		})
	}
}

func TestPages_notServed(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodGet, "/foo", http.StatusNotFound},
		{http.MethodGet, "/privacy/", http.StatusNotFound},
		{http.MethodGet, "/privacy/x", http.StatusNotFound},
		{http.MethodGet, "/terms/", http.StatusNotFound},
		{http.MethodGet, "/index.html", http.StatusNotFound},
		{http.MethodGet, "/Privacy", http.StatusNotFound},
		{http.MethodPost, "/privacy", http.StatusMethodNotAllowed},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodPut, "/terms", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/privacy", http.StatusMethodNotAllowed},
	}
	mux := newMux(t, testEmail)
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			rec := do(t, mux, tt.method, tt.path)

			require.Equal(t, tt.want, rec.Code)
			require.NotContains(t, rec.Body.String(), "Most Files")
		})
	}
}

// The home page is registered at the root; it must not shadow the routes the
// rest of the app mounts, whatever the registration order.
func TestPages_doesNotSwallowOtherRoutes(t *testing.T) {
	pages, err := web.New(web.Config{ContactEmail: testEmail})
	require.NoError(t, err)

	other := func(mux *http.ServeMux) {
		for _, pattern := range []string{"GET /healthz", "GET /oauth/google/callback", "POST /telegram/webhook"} {
			mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("other")) })
		}
	}
	orders := map[string]func(*http.ServeMux){
		"pages first": func(m *http.ServeMux) { pages.RegisterRoutes(m); other(m) },
		"pages last":  func(m *http.ServeMux) { other(m); pages.RegisterRoutes(m) },
	}
	tests := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/healthz", "other"},
		{http.MethodGet, "/oauth/google/callback", "other"},
		{http.MethodPost, "/telegram/webhook", "other"},
	}
	for name, register := range orders {
		mux := http.NewServeMux()
		register(mux)
		for _, tt := range tests {
			t.Run(name+" "+tt.path, func(t *testing.T) {
				rec := do(t, mux, tt.method, tt.path)

				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, tt.want, rec.Body.String())
			})
		}
		t.Run(name+" unknown stays 404", func(t *testing.T) {
			require.Equal(t, http.StatusNotFound, do(t, mux, http.MethodGet, "/nope").Code)
		})
	}
}

func TestPages_escapesContactEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
	}{
		{"tag and quote", `a<b>@x.com"`},
		{"attribute breakout with script", `x"><script>@a.b`},
		{"single quote handler", `'><img src=x onerror=alert(1)>@a.b`},
		{"ampersand", `a&b@x.com`},
	}
	for _, tt := range tests {
		mux := newMux(t, tt.email)
		for _, path := range []string{"/", "/privacy", "/terms"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				body := do(t, mux, http.MethodGet, path).Body.String()

				for _, s := range []string{"<script", "<img", "<b>"} {
					require.NotContains(t, strings.ToLower(body), s)
				}
				hrefs := mailtoRe.FindAllStringSubmatch(body, -1)
				require.NotEmpty(t, hrefs)
				for _, m := range hrefs {
					require.NotContains(t, m[1], "<")
					require.NotContains(t, m[1], ">")
					require.NotContains(t, m[1], "'")
				}
				// The visible text is the address as given.
				require.Contains(t, html.UnescapeString(tagRe.ReplaceAllString(body, "")), tt.email)
			})
		}
	}
}
