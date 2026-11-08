package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

// Client returns an *http.Client that authenticates as the user and refreshes
// the access token automatically. A revoked grant surfaces as ErrAccessRevoked.
func (s *Service) Client(ctx context.Context, userID, bot string) (*http.Client, error) {
	src, err := s.tokenSource(ctx, userID, bot)
	if err != nil {
		return nil, err
	}
	return oauth2.NewClient(ctx, src), nil
}

// tokenSource loads and decrypts the refresh token, then forces one refresh so
// a revoked grant is detected now rather than mid-request. On invalid_grant the
// integration is marked revoked and ErrAccessRevoked is returned.
func (s *Service) tokenSource(ctx context.Context, userID, bot string) (oauth2.TokenSource, error) {
	ig, err := s.store.Integration(ctx, userID, bot, provider)
	if err != nil {
		return nil, err
	}
	if ig.Status == statusRevoked {
		return nil, ErrAccessRevoked
	}

	refresh, err := s.enc.decrypt(ig.RefreshTokenEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}

	src := s.config().TokenSource(ctx, &oauth2.Token{RefreshToken: string(refresh)})
	if _, err := src.Token(); err != nil {
		if isInvalidGrant(err) {
			if uErr := s.store.UpdateIntegrationStatus(ctx, userID, bot, provider, statusRevoked); uErr != nil {
				s.log.Error("mark integration revoked", "err", uErr, "user_id", userID)
			}
			return nil, ErrAccessRevoked
		}
		return nil, fmt.Errorf("refresh access token: %w", err)
	}
	return src, nil
}

// Revoke revokes the user's token at Google and marks the integration revoked.
func (s *Service) Revoke(ctx context.Context, userID, bot string) error {
	ig, err := s.store.Integration(ctx, userID, bot, provider)
	if err != nil {
		return err
	}
	refresh, err := s.enc.decrypt(ig.RefreshTokenEnc)
	if err != nil {
		return fmt.Errorf("decrypt refresh token: %w", err)
	}
	if err := s.revokeAtGoogle(ctx, string(refresh)); err != nil {
		return err
	}
	if err := s.store.UpdateIntegrationStatus(ctx, userID, bot, provider, statusRevoked); err != nil {
		return fmt.Errorf("mark integration revoked: %w", err)
	}
	return nil
}

// RevokeBestEffort revokes the user's token at Google and guarantees the
// integration ends up marked revoked locally, even when Google is unreachable or
// the token is already gone. A Google-side failure is logged and swallowed; only a
// failure to mark the integration revoked in the store is returned. A missing
// integration is a no-op.
func (s *Service) RevokeBestEffort(ctx context.Context, userID, bot string) error {
	err := s.Revoke(ctx, userID, bot)
	if err == nil || errors.Is(err, ErrNoIntegration) {
		return nil
	}
	s.log.Warn("drive revoke failed; forcing local revoke", "err", err, "user_id", userID)
	if mErr := s.store.UpdateIntegrationStatus(ctx, userID, bot, provider, statusRevoked); mErr != nil {
		return fmt.Errorf("mark integration revoked: %w", mErr)
	}
	return nil
}

func (s *Service) revokeAtGoogle(ctx context.Context, token string) error {
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build revoke request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke at google: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Google answers 200 on success and 400 when the token is already invalid;
	// both leave it unusable, so only a 5xx is a real failure.
	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("revoke at google: status %d", resp.StatusCode)
	}
	return nil
}

// isInvalidGrant reports whether err is Google's "invalid_grant" response,
// meaning the refresh token is no longer valid.
func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return re.ErrorCode == "invalid_grant"
	}
	return false
}
