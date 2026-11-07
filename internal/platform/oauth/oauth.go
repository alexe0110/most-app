// Package oauth runs the Google OAuth flow: build the consent URL (one-time
// state + PKCE), handle the callback, store an encrypted refresh token, and
// hand out authenticated HTTP clients. Storage is via the Store port, which a
// bot's postgres adapter implements, so platform never imports a bot.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	provider       = "google"
	scopeDriveFile = "https://www.googleapis.com/auth/drive.file"

	statusActive  = "active"
	statusRevoked = "revoked"

	// CallbackPath is where the OAuth redirect lands on the shared HTTP mux.
	CallbackPath = "/oauth/google/callback"

	stateTTL         = 10 * time.Minute
	stateBytes       = 32
	defaultRevokeURL = "https://oauth2.googleapis.com/revoke"
)

// Config carries the OAuth settings resolved from the environment.
type Config struct {
	ClientID     string
	ClientSecret string
	TokenEncKey  string // base64-encoded 32-byte key for refresh-token encryption
	PublicURL    string // base URL; the callback redirect URI is derived from it
	BotURL       string // t.me deep link the user is sent back to after consent
}

// Service runs the OAuth flow. Construct it with New.
type Service struct {
	clientID     string
	clientSecret string
	publicURL    string
	botURL       string
	store        Store
	onConnected  OnConnected
	enc          *encryptor
	log          *slog.Logger
	endpoint     oauth2.Endpoint
	revokeURL    string
	httpClient   *http.Client
}

// Option overrides a Service default. Used by tests to point at a fake Google.
type Option func(*Service)

// WithEndpoint overrides the Google OAuth endpoint (auth and token URLs).
func WithEndpoint(e oauth2.Endpoint) Option { return func(s *Service) { s.endpoint = e } }

// WithRevokeURL overrides the token-revocation endpoint.
func WithRevokeURL(u string) Option { return func(s *Service) { s.revokeURL = u } }

// New validates the config and builds the Service. onConnected may be nil.
func New(cfg Config, store Store, onConnected OnConnected, log *slog.Logger, opts ...Option) (*Service, error) {
	enc, err := newEncryptor(cfg.TokenEncKey)
	if err != nil {
		return nil, fmt.Errorf("token cipher: %w", err)
	}
	s := &Service{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		publicURL:    strings.TrimRight(cfg.PublicURL, "/"),
		botURL:       cfg.BotURL,
		store:        store,
		onConnected:  onConnected,
		enc:          enc,
		log:          log,
		endpoint:     google.Endpoint,
		revokeURL:    defaultRevokeURL,
		httpClient:   http.DefaultClient,
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// config is the oauth2 config for the current request.
func (s *Service) config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.clientID,
		ClientSecret: s.clientSecret,
		Endpoint:     s.endpoint,
		RedirectURL:  s.publicURL + CallbackPath,
		Scopes:       []string{scopeDriveFile},
	}
}

// AuthURL stores a one-time state with PKCE and returns the Google consent URL.
// offline + consent make Google return a refresh token on every consent.
func (s *Service) AuthURL(ctx context.Context, userID, bot string) (string, error) {
	state, err := randomToken(stateBytes)
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	verifier := oauth2.GenerateVerifier()

	if err := s.store.SaveState(ctx, State{
		Value:     state,
		UserID:    userID,
		Bot:       bot,
		Verifier:  verifier,
		ExpiresAt: time.Now().Add(stateTTL),
	}); err != nil {
		return "", fmt.Errorf("save state: %w", err)
	}

	return s.config().AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	), nil
}

// Connected reports whether the user has an active (non-revoked) integration.
func (s *Service) Connected(ctx context.Context, userID, bot string) (bool, error) {
	ig, err := s.store.Integration(ctx, userID, bot, provider)
	if errors.Is(err, ErrNoIntegration) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ig.Status == statusActive, nil
}

// randomToken returns n cryptographically random bytes, URL-safe base64 encoded.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
