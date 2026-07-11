package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// NewAuthentikMux returns the authentik-mode config routes: GET status, PUT
// url/client id/client secret. Kept on its own small mux, mirroring
// NewForwardMux/NewAuthModeMux's precedent — this mutates security-relevant
// state (or, for GET, exposes config that must never include the secret
// itself), so it must be session-protected. cmd/sakms wraps it in the same
// auth.Middleware as the other protected muxes.
//
// This is SEPARATE from the first-run bootstrap path (authSetupHandler's
// "authentik" branch in auth.go) — these routes are for changing an
// ALREADY-configured instance's authentik config (the Settings panel's
// "switch to authentik" or "rotate credentials" actions), reachable only
// once the operator already holds a session cookie or the universal API key
// (see the plan's §0.7/§3.4).
func NewAuthentikMux(authStore *auth.Store, secretEnc auth.TokenEncryptor) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auth/authentik", authentikGetHandler(authStore))
	mux.HandleFunc("PUT /api/auth/authentik", authentikPutHandler(authStore, secretEnc))
	return mux
}

// authentikStatusResponse never includes the client secret itself (G6) —
// only the url, client id, and whether a secret is currently configured.
type authentikStatusResponse struct {
	URL       string `json:"url"`
	ClientID  string `json:"clientId"`
	HasSecret bool   `json:"hasSecret"`
}

func authentikGetHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url, clientID, cipher, err := authStore.AuthentikConfig(r.Context())
		if err != nil {
			log.Printf("authentik status: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authentikStatusResponse{
			URL:       url,
			ClientID:  clientID,
			HasSecret: cipher != "",
		})
	}
}

type authentikConfigRequest struct {
	URL          string `json:"url"`
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// authentikPutHandler encrypts ClientSecret via secretEnc before it ever
// reaches settings.Set — the client secret is an outbound credential SAK
// presents to Authentik (encrypted at rest, decrypted only at introspection
// time), not a one-way hash like the password or forward secret.
func authentikPutHandler(authStore *auth.Store, secretEnc auth.TokenEncryptor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req authentikConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		url := strings.TrimSpace(req.URL)
		clientID := strings.TrimSpace(req.ClientID)
		clientSecret := req.ClientSecret
		if url == "" || clientID == "" || clientSecret == "" {
			http.Error(w, "url, clientId, and clientSecret are all required", http.StatusBadRequest)
			return
		}
		cipher, err := secretEnc.Encrypt(clientSecret)
		if err != nil {
			log.Printf("authentik config encrypt: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := authStore.SetAuthentikConfig(r.Context(), url, clientID, cipher); err != nil {
			log.Printf("authentik config set: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
