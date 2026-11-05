package oauth

import "errors"

var (
	// ErrStateNotFound means the callback state is unknown or already used.
	ErrStateNotFound = errors.New("oauth: state not found")
	// ErrStateExpired means the state outlived its TTL.
	ErrStateExpired = errors.New("oauth: state expired")
	// ErrNoIntegration means the user has not connected the provider.
	ErrNoIntegration = errors.New("oauth: no integration")
	// ErrAccessRevoked means Google rejected the refresh token; the user must
	// reconnect. This is the domain-level meaning surfaced to callers.
	ErrAccessRevoked = errors.New("oauth: access revoked")
	// ErrNoRefreshToken means Google returned no refresh token, so offline
	// access cannot be persisted.
	ErrNoRefreshToken = errors.New("oauth: no refresh token returned")
)
