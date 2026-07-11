package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// NewAuthMux returns the handful of routes that must stay reachable without
// a session — setup, login, logout, and status — kept on their OWN mux,
// deliberately separate from NewMux's business-logic routes. cmd/sakms
// wraps NewMux's result in auth.Middleware but mounts this one unwrapped;
// keeping them apart means that middleware never needs an exemption list,
// and NewMux's own large existing test suite never has to know auth exists
// at all.
func NewAuthMux(authStore *auth.Store, tokenEnc auth.TokenEncryptor) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/setup", authSetupHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/login", authLoginHandler(authStore, tokenEnc))
	mux.HandleFunc("POST /api/auth/logout", authLogoutHandler())
	mux.HandleFunc("GET /api/auth/status", authStatusHandler(authStore, tokenEnc))
	return mux
}

type authCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Mode selects the auth strategy at first run — "" means "password"
	// (today's exact back-compat behavior). "authentik" is accepted here
	// too (per the plan's first-run bootstrap fix: its config can't go
	// through a protected endpoint before any credential exists) but
	// returns a 400 placeholder until slice 3 lands.
	Mode string `json:"mode"`
	// AcknowledgeInsecure must be true to select Mode "none" — a genuine
	// no-auth instance requires an explicit, unmissable opt-in (G2).
	AcknowledgeInsecure bool `json:"acknowledgeInsecure"`
	// ForwardSecret is optional for a "forward"-mode setup request — if
	// empty, the handler generates one server-side (simpler UX than
	// requiring the frontend to do its own crypto-random generation). If
	// non-empty, the operator-supplied value is persisted as-is. Ignored
	// for every other mode.
	ForwardSecret string `json:"forwardSecret,omitempty"`
}

// authSetupResponse is the JSON body returned by authSetupHandler for modes
// that must hand something back to the caller — currently just "forward",
// whose generated-or-accepted shared secret is revealed here ONCE (G6) so
// the operator can copy it into their reverse-proxy config immediately.
// Empty for "password"/"none", which still respond with a bare 204.
type authSetupResponse struct {
	ForwardSecret string `json:"forwardSecret,omitempty"`
}

// authSetupHandler creates SAK's one login — refuses once a login
// already exists (checked fresh on every call, not cached) so a visitor who
// reaches an already-configured instance can't silently take it over by
// "setting up" a login of their own; they need /api/auth/login instead.
func authSetupHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		configured, err := authStore.Configured(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if configured {
			http.Error(w, "a login is already configured — use /api/auth/login instead", http.StatusConflict)
			return
		}

		var req authCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		mode := req.Mode
		if mode == "" {
			mode = auth.ModePassword // back-compat: today's exact default
		}

		switch mode {
		case auth.ModePassword:
			if err := authStore.SetCredentials(ctx, req.Username, req.Password); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := authStore.SetAuthMode(ctx, auth.ModePassword); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			token, err := auth.IssueToken(tokenEnc)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			auth.SetSessionCookie(w, token)
			w.WriteHeader(http.StatusNoContent)
		case auth.ModeNone:
			if !req.AcknowledgeInsecure {
				http.Error(w, "acknowledgeInsecure must be true to select the none auth mode", http.StatusBadRequest)
				return
			}
			// No credentials, no cookie — "none" mode has nothing to
			// authenticate.
			if err := authStore.SetAuthMode(ctx, auth.ModeNone); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case auth.ModeForward:
			// First-run bootstrap (plan §0.7/§2.2b): carried in this same
			// public setup body, not a protected config endpoint, because
			// no credential exists yet to authenticate against one. Generate
			// a secret server-side unless the operator supplied their own;
			// persist it, THEN write auth_mode — atomically, one request.
			rawSecret := strings.TrimSpace(req.ForwardSecret)
			if rawSecret == "" {
				generated, genErr := authStore.GenerateForwardSecret(ctx)
				if genErr != nil {
					http.Error(w, genErr.Error(), http.StatusInternalServerError)
					return
				}
				rawSecret = generated
			} else if err := authStore.SetForwardSecret(ctx, rawSecret); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if err := authStore.SetAuthMode(ctx, auth.ModeForward); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// No cookie — forward mode has no cookie concept. The secret is
			// shown ONCE here (G6); there is no later endpoint that can ever
			// retrieve it again.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(authSetupResponse{ForwardSecret: rawSecret})
		case auth.ModeAuthentik:
			// Slice-1 placeholder (plan §0.7/§1.3): slice 3 replaces this
			// branch with real first-run config handling carried in this
			// same public setup body.
			http.Error(w, "mode not selectable yet", http.StatusBadRequest)
		default:
			http.Error(w, "unknown auth mode", http.StatusBadRequest)
		}
	}
}

func authLoginHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		mode, err := authStore.AuthMode(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if mode != auth.ModePassword {
			// No cookie concept in forward/authentik/none — minting one
			// here would create exactly the stale-cookie path Edge Case #3
			// forbids.
			http.Error(w, "login is not applicable in the current auth mode", http.StatusBadRequest)
			return
		}

		var req authCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		ok, err := authStore.Verify(ctx, req.Username, req.Password)
		if err != nil && !errors.Is(err, auth.ErrNotConfigured) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
			return
		}

		token, err := auth.IssueToken(tokenEnc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		auth.SetSessionCookie(w, token)
		w.WriteHeader(http.StatusNoContent)
	}
}

// authLogoutHandler always succeeds — clearing a cookie that may not exist
// is harmless, and there's no server-side session state to invalidate (see
// session.go's doc comment on why tokens are stateless).
func authLogoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	}
}

type authStatusResponse struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Mode          string `json:"mode"`
}

// authStatusHandler is the one endpoint the frontend calls before it knows
// anything else about the instance — it decides which of "create your
// login," "log in," or "proceed" to show. Authenticated is computed
// relative to the active mode: "none" is always true (nothing to check),
// "password" is today's cookie check unchanged, "forward" calls
// auth.ForwardAuth directly for a REAL per-request check (plan §3.3's
// critic-fix: safe here because the check is purely local — a settings
// read + constant-time compare, no outbound call, no amplification
// concern — unlike authentik mode's RFC 7662 introspection (slice 3),
// which will use a presence-only heuristic here instead to avoid handing
// an unauthenticated caller a free introspection call per request).
// authentik isn't reachable as the active mode until slice 3 (setup's
// placeholder branch above refuses to select it), so its status branch
// lands alongside that slice's helper — the default case below is a safe
// fallback (today's cookie check) until then.
func authStatusHandler(authStore *auth.Store, tokenEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		configured, err := authStore.Configured(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mode, err := authStore.AuthMode(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var authenticated bool
		switch mode {
		case auth.ModeNone:
			authenticated = true
		case auth.ModePassword:
			authenticated = auth.Authenticated(tokenEnc, r)
		case auth.ModeForward:
			authenticated, err = auth.ForwardAuth(authStore, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		default:
			// authentik's presence-only status branch lands in slice 3.
			authenticated = auth.Authenticated(tokenEnc, r)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authStatusResponse{
			Configured:    configured,
			Authenticated: authenticated,
			Mode:          mode,
		})
	}
}
