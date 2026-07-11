package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// NewForwardMux returns the forward-mode config routes: GET status,
// POST-generate/regenerate the shared secret, PUT the header names. Kept on
// its own small mux, mirroring NewAPIKeyMux/NewAuthModeMux's precedent —
// this mutates security-relevant state (or, for GET, exposes config that
// must never include the secret itself), so it must be session-protected.
// cmd/sakms wraps it in the same auth.Middleware as the other protected
// muxes.
//
// This is SEPARATE from the first-run bootstrap path (authSetupHandler's
// "forward" branch in auth.go) — these routes are for changing an
// ALREADY-configured instance's forward config (the Settings panel's
// "switch to forward-auth" or "rotate the shared secret" actions),
// reachable only once the operator already holds a session cookie or the
// universal API key (see the plan's §0.7/§2.3).
func NewForwardMux(authStore *auth.Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/forward", forwardGetHandler(authStore))
	mux.HandleFunc("POST /api/auth/forward/secret", forwardGenerateSecretHandler(authStore))
	mux.HandleFunc("PUT /api/auth/forward/headers", forwardSetHeadersHandler(authStore))
	return mux
}

// forwardStatusResponse never includes the secret itself (G6) — only
// whether one is configured, plus the two header names currently in
// effect.
type forwardStatusResponse struct {
	HasSecret    bool   `json:"hasSecret"`
	UserHeader   string `json:"userHeader"`
	SecretHeader string `json:"secretHeader"`
}

func forwardGetHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		hasSecret, err := authStore.ForwardConfigured(ctx)
		if err != nil {
			log.Printf("forward status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		userHeader, secretHeader, err := authStore.ForwardHeaders(ctx)
		if err != nil {
			log.Printf("forward status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(forwardStatusResponse{
			HasSecret:    hasSecret,
			UserHeader:   userHeader,
			SecretHeader: secretHeader,
		})
	}
}

// forwardSecretResponse is the one place the full forward secret crosses
// the API boundary post-first-run — shown once, never retrievable again
// afterward, mirroring apikeyRegenerateResponse's shape.
type forwardSecretResponse struct {
	Secret string `json:"secret"`
}

func forwardGenerateSecretHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := authStore.GenerateForwardSecret(r.Context())
		if err != nil {
			log.Printf("forward secret generate: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(forwardSecretResponse{Secret: raw})
	}
}

type forwardHeadersRequest struct {
	UserHeader   string `json:"userHeader"`
	SecretHeader string `json:"secretHeader"`
}

func forwardSetHeadersHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req forwardHeadersRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		userHeader := strings.TrimSpace(req.UserHeader)
		secretHeader := strings.TrimSpace(req.SecretHeader)
		if userHeader == "" || secretHeader == "" {
			http.Error(w, "userHeader and secretHeader are both required", http.StatusBadRequest)
			return
		}
		if err := authStore.SetForwardHeaders(r.Context(), userHeader, secretHeader); err != nil {
			log.Printf("forward headers set: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
