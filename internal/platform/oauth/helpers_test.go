package oauth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func discardLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeStore is an in-memory Store for unit tests.
type fakeStore struct {
	states       map[string]State
	integrations map[string]Integration // keyed by userID|bot|provider
}

func newFakeStore() *fakeStore {
	return &fakeStore{states: map[string]State{}, integrations: map[string]Integration{}}
}

func igKey(userID, bot, provider string) string { return userID + "|" + bot + "|" + provider }

func (f *fakeStore) SaveState(_ context.Context, s State) error {
	f.states[s.Value] = s
	return nil
}

func (f *fakeStore) TakeState(_ context.Context, state string) (State, error) {
	s, ok := f.states[state]
	if !ok {
		return State{}, ErrStateNotFound
	}
	delete(f.states, state)
	return s, nil
}

func (f *fakeStore) SaveIntegration(_ context.Context, userID, bot, provider, status string, enc []byte) error {
	f.integrations[igKey(userID, bot, provider)] = Integration{RefreshTokenEnc: enc, Status: status}
	return nil
}

func (f *fakeStore) Integration(_ context.Context, userID, bot, provider string) (Integration, error) {
	ig, ok := f.integrations[igKey(userID, bot, provider)]
	if !ok {
		return Integration{}, ErrNoIntegration
	}
	return ig, nil
}

func (f *fakeStore) UpdateIntegrationStatus(_ context.Context, userID, bot, provider, status string) error {
	ig, ok := f.integrations[igKey(userID, bot, provider)]
	if !ok {
		return ErrNoIntegration
	}
	ig.Status = status
	f.integrations[igKey(userID, bot, provider)] = ig
	return nil
}

// fakeGoogle stands in for Google's token and revoke endpoints.
type fakeGoogle struct {
	invalidGrant bool // refresh returns invalid_grant
	omitRefresh  bool // the authorization_code exchange omits a refresh token
	revokeStatus int  // status for /revoke (0 => 200)
}

// refreshPlaintext is the token the fake hands out on a code exchange; tests
// assert it never appears in storage unencrypted.
const refreshPlaintext = "refresh-plaintext"

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch {
	case strings.HasSuffix(r.URL.Path, "/token"):
		grant := r.FormValue("grant_type")
		if grant == "refresh_token" && f.invalidGrant {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body := `{"access_token":"access-xyz","token_type":"Bearer","expires_in":3600`
		if grant == "authorization_code" && !f.omitRefresh {
			body += `,"refresh_token":"` + refreshPlaintext + `"`
		}
		body += `}`
		_, _ = io.WriteString(w, body)
	case strings.HasSuffix(r.URL.Path, "/revoke"):
		status := f.revokeStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// newTestService builds a Service pointed at a fake Google server.
func newTestService(t *testing.T, store Store, onConnected OnConnected, g *fakeGoogle) *Service {
	t.Helper()
	ts := httptest.NewServer(g)
	t.Cleanup(ts.Close)

	svc, err := New(Config{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		TokenEncKey:  keyB64(t, 0x09),
		PublicURL:    "https://most.example",
		BotURL:       "https://t.me/most_files_bot",
	}, store, onConnected, discardLog(),
		WithEndpoint(oauth2.Endpoint{AuthURL: ts.URL + "/auth", TokenURL: ts.URL + "/token"}),
		WithRevokeURL(ts.URL+"/revoke"),
	)
	require.NoError(t, err)
	return svc
}

// doCallback runs the callback handler with the given raw query (leading "?").
func doCallback(t *testing.T, svc *Service, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, CallbackPath+rawQuery, nil)
	rec := httptest.NewRecorder()
	svc.Callback()(rec, req)
	return rec
}
