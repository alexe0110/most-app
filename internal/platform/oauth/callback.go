package oauth

import (
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

// Callback handles GET /oauth/google/callback. The user is always redirected
// back to the bot; failures are logged with a reason, never with tokens, code
// or state. This is the one place this flow logs errors.
func (s *Service) Callback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()

		if reason := q.Get("error"); reason != "" {
			// The user declined consent (or Google refused). reason is a short
			// code such as "access_denied" — safe to log.
			s.log.InfoContext(ctx, "oauth consent not granted", "reason", reason)
			s.redirect(w, r)
			return
		}

		code := q.Get("code")
		state := q.Get("state")
		if code == "" || state == "" {
			s.log.WarnContext(ctx, "oauth callback missing code or state")
			s.redirect(w, r)
			return
		}

		st, err := s.store.TakeState(ctx, state)
		if err != nil {
			// Unknown, already used, or otherwise invalid state.
			s.log.WarnContext(ctx, "oauth callback rejected", "err", err)
			s.redirect(w, r)
			return
		}
		if time.Now().After(st.ExpiresAt) {
			s.log.WarnContext(ctx, "oauth callback rejected", "err", ErrStateExpired)
			s.redirect(w, r)
			return
		}

		tok, err := s.config().Exchange(s.tokenCtx(ctx), code, oauth2.VerifierOption(st.Verifier))
		if err != nil {
			s.log.ErrorContext(ctx, "oauth code exchange failed", "err", err)
			s.redirect(w, r)
			return
		}
		if tok.RefreshToken == "" {
			s.log.ErrorContext(ctx, "oauth exchange returned no refresh token", "user_id", st.UserID)
			s.redirect(w, r)
			return
		}

		enc, err := s.enc.encrypt([]byte(tok.RefreshToken))
		if err != nil {
			s.log.ErrorContext(ctx, "encrypt refresh token", "err", err)
			s.redirect(w, r)
			return
		}
		if err := s.store.SaveIntegration(ctx, st.UserID, st.Bot, provider, statusActive, enc); err != nil {
			s.log.ErrorContext(ctx, "save integration", "err", err, "user_id", st.UserID)
			s.redirect(w, r)
			return
		}

		if s.onConnected != nil {
			if err := s.onConnected(ctx, st.UserID); err != nil {
				// Non-fatal: the integration is stored; the user just missed the
				// Telegram ping.
				s.log.ErrorContext(ctx, "notify connected", "err", err, "user_id", st.UserID)
			}
		}

		s.redirect(w, r)
	}
}

// redirect sends the user back to the bot. http.Redirect sets 302 Found.
func (s *Service) redirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.botURL, http.StatusFound)
}
