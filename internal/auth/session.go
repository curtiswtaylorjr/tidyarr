package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/authentik"
)

// CookieName is the session cookie the browser carries on every request
// after a successful login/setup.
const CookieName = "sakms_session"

// sessionTTL is long-lived on purpose: SAK is a single-operator,
// self-hosted tool where a forced re-login every few hours is friction with
// no real security benefit (the credential itself, not session length, is
// the actual boundary) — 30 days means "log in occasionally," not "log in
// every visit."
const sessionTTL = 30 * 24 * time.Hour

// TokenEncryptor is the same Encrypt/Decrypt shape internal/secrets.Store
// already provides for API-key-at-rest encryption — session tokens reuse it
// rather than a second crypto primitive: AES-256-GCM authenticated
// encryption means a tampered or expired token simply fails to decrypt (or
// fails its own expiry check after decrypting), with no separate
// signature-verification code path to get wrong.
type TokenEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

type sessionPayload struct {
	Exp int64 `json:"exp"`
}

// IssueToken returns an opaque, tamper-evident session token (safe to use
// as a cookie value) valid for sessionTTL from now.
func IssueToken(enc TokenEncryptor) (string, error) {
	payload := sessionPayload{Exp: time.Now().Add(sessionTTL).Unix()}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return enc.Encrypt(string(data))
}

// ValidateToken reports whether token is a genuine, unexpired session
// token — false for anything tampered, corrupted, encrypted under a
// different key, or past its expiry.
func ValidateToken(enc TokenEncryptor, token string) bool {
	data, err := enc.Decrypt(token)
	if err != nil {
		return false
	}
	var payload sessionPayload
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false
	}
	return time.Now().Unix() < payload.Exp
}

// SetSessionCookie writes a fresh session cookie to w.
//
// Secure isn't set: SAK's primary deployment is a self-hosted instance
// on a trusted LAN, often reached over plain HTTP the same way Radarr/
// Sonarr/Whisparr themselves are — forcing Secure would silently break the
// cookie (and therefore all login) for that entirely normal setup. Anyone
// exposing SAK beyond a trusted network should put a TLS-terminating
// reverse proxy in front of it, same guidance as those apps.
func SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(sessionTTL),
	})
}

// ClearSessionCookie removes the session cookie (logout) — an Expires in
// the past is the standard way to tell the browser to drop a cookie
// immediately, since there's no server-side session state to invalidate.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Unix(0, 0), MaxAge: -1,
	})
}

// Authenticated reports whether r carries a valid session cookie.
func Authenticated(enc TokenEncryptor, r *http.Request) bool {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	return ValidateToken(enc, cookie.Value)
}

// Middleware gates every request to next according to the instance's
// active auth mode (password/forward/authentik/none) — meant to wrap the
// business-logic API mux only; the auth endpoints themselves (setup/login/
// logout/status) live on a separate, always-public mux that never passes
// through this (see internal/api.NewAuthMux), so there's no exemption list
// to keep in sync here.
//
// Dispatch order (deliberate, do not reorder):
//  1. Read the effective mode via store.AuthMode. Any error (not the
//     unset→"password" default, a genuine store failure) fails CLOSED
//     (500) before anything else runs — G1, "the store couldn't tell us"
//     must never be treated as a passing default.
//  2. "none" short-circuits here, before the key check below — so a
//     key-store hiccup can never 500 an explicitly no-auth mode.
//  3. The universal X-Api-Key header is checked next, INDEPENDENT of which
//     mode is active (Human Decision #2: the key works in every mode, not
//     just password) — finalAllow = keyOK || modeSpecificOK. This check
//     lives here, in Middleware's own body, deliberately NOT inside any
//     per-mode helper, so no future mode addition can accidentally scope it
//     to one mode.
//  4. Only if the key didn't pass does a mode-specific credential get
//     checked (cookie for password, etc). The password branch is
//     cookie-ONLY (passwordAuth) — the key is no longer localized to this
//     branch, it already had its shot in step 3 — and a session cookie is
//     honored ONLY in the password branch (Edge Case #3: a stale cookie
//     must never authenticate a request once the active mode is
//     forward/authentik/none).
//
// A mode-read or credential-check store error fails CLOSED (500), never
// falls through to allow.
func Middleware(enc TokenEncryptor, store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode, err := store.AuthMode(r.Context())
		if err != nil { // G1 fail-closed
			http.Error(w, "authentication error", http.StatusInternalServerError)
			return
		}
		// none short-circuits BEFORE the key check so a key-store hiccup
		// can't 500 an explicitly no-auth mode. Represented here directly
		// (no separate noneAuth function) so a reader sees the ordering
		// guarantee in one place rather than hunting for it.
		if mode == ModeNone {
			next.ServeHTTP(w, r)
			return
		}
		// UNIVERSAL X-Api-Key credential, checked here in the top-level
		// body, independent of the mode switch — Human Decision #2. Do NOT
		// move this into any per-mode helper.
		presented := strings.TrimSpace(r.Header.Get("X-Api-Key"))
		if presented != "" {
			ok, err := store.VerifyAPIKey(r.Context(), presented)
			if err != nil {
				http.Error(w, "authentication error", http.StatusInternalServerError)
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
		}
		// Mode-specific credential.
		var allowed bool
		switch mode {
		case ModePassword:
			allowed = passwordAuth(enc, r)
		case ModeForward:
			allowed, err = ForwardAuth(store, r)
		case ModeAuthentik:
			allowed, err = AuthentikAuth(r.Context(), store, r)
		default: // unknown/corrupt mode → fail closed
			allowed = false
		}
		if err != nil {
			http.Error(w, "authentication error", http.StatusInternalServerError)
			return
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "authentication required", http.StatusUnauthorized)
	})
}

// passwordAuth is the cookie-only check for password mode (Edge Case #3: a
// session cookie is honored ONLY in this branch, never in forward/
// authentik/none). The X-Api-Key path is deliberately NOT here — it is
// universal and lives in Middleware's own body above, checked before this
// helper ever runs.
func passwordAuth(enc TokenEncryptor, r *http.Request) bool {
	return Authenticated(enc, r)
}

// ForwardAuth is the forward-mode check: a constant-time compare of the
// configured secret header's value against the stored forward-secret hash.
// Exported (unlike passwordAuth) because internal/api's status handler also
// calls it directly for a REAL per-request check — per the plan's §3.3
// critic-fix, this is safe to do from the public status endpoint because
// the check is purely local (a settings read + subtle.ConstantTimeCompare,
// no outbound network call), unlike authentik mode's RFC 7662 introspection
// (slice 3), which carries an amplification concern the status endpoint
// must avoid by using a presence-only heuristic instead. Calling the exact
// same function from both Middleware and the status handler guarantees the
// status result reflects the real gate, not a parallel reimplementation
// that could drift into a heuristic.
func ForwardAuth(store *Store, r *http.Request) (bool, error) {
	_, secretHeader, err := store.ForwardHeaders(r.Context())
	if err != nil {
		return false, err
	}
	presented := strings.TrimSpace(r.Header.Get(secretHeader))
	ok, err := store.VerifyForwardSecret(r.Context(), presented)
	if err != nil {
		return false, err // fail closed (G1/G5)
	}
	// The identity header is intentionally never read here: its value is
	// cosmetic in single-operator SAK (Scope Risk #2) — presence/logging
	// only, never the authorization gate. Authorization is entirely
	// determined by the secret-header compare above.
	return ok, nil
}

// authentikClient builds an internal/authentik.Client from the stored,
// decrypted Authentik config — the one place the client secret's ciphertext
// (auth_authentik_client_secret_enc) is ever decrypted, using this Store's
// own enc field (see auth.go's New).
//
// Return shape distinguishes two different failure classes, matching the
// rest of this file's convention (see ForwardAuth/VerifyForwardSecret): a
// nil client with a nil error means "authentik mode is active but has no
// config yet" — a legitimate not-configured state (mirrors
// VerifyForwardSecret's "not configured" → false, no error), which
// AuthentikAuth turns into a plain 401, not a 500. A non-nil error means a
// genuine internal fault (the settings store itself is broken, or the
// stored ciphertext no longer decrypts under the current secret key) — that
// fails closed via Middleware's existing G1 500 path, the same way a broken
// settings store already does for every other mode.
func (s *Store) authentikClient(ctx context.Context) (*authentik.Client, error) {
	url, clientID, cipher, err := s.AuthentikConfig(ctx)
	if err != nil {
		return nil, err
	}
	if url == "" || clientID == "" || cipher == "" {
		return nil, nil // not configured — not a store fault
	}
	if s.enc == nil {
		return nil, errors.New("auth: no secret decryptor configured for authentik mode")
	}
	secret, err := s.enc.Decrypt(cipher)
	if err != nil {
		return nil, err
	}
	return authentik.New(authentik.Config{URL: url, ClientID: clientID, ClientSecret: secret}, s.httpClient), nil
}

// AuthentikAuth is the authentik-mode check: RFC 7662 token introspection
// against a presented `Authorization: Bearer <token>` header. Exported
// (mirroring ForwardAuth's naming convention), but — UNLIKE ForwardAuth —
// must NEVER be called from the public status endpoint's per-request check.
// ForwardAuth's check is a purely local, cheap subtle.ConstantTimeCompare
// against a stored hash; this one makes a real outbound HTTP call to
// Authentik, so calling it from an unauthenticated, attacker-rate-controlled
// endpoint (the public /api/auth/status) would be a real amplification
// vector against Authentik itself (plan §3.3's critic-driven fix) — the
// status endpoint uses a cheaper presence-only heuristic instead (see
// internal/api's authStatusHandler). This function remains the one true,
// fully-enforced gate for every actual protected API request via Middleware.
//
// AC4/G5: an active token passes; an inactive token, a failed/timed-out
// introspection call, or an unconfigured instance all deny with a plain
// 401 (returned error is nil) — Authentik being unreachable is a fact about
// an EXTERNAL service, not "our own store couldn't tell us" (that
// distinction is what reserves Middleware's 500 path for a genuine local
// store/decrypt fault, via the non-nil error returned by store.authentikClient
// above).
func AuthentikAuth(ctx context.Context, store *Store, r *http.Request) (bool, error) {
	token := BearerToken(r) // case-insensitive "Bearer" scheme match, per RFC 7235 §2.1 (Phase 4 fix-up)
	if token == "" {
		return false, nil // empty/whitespace bearer treated as absent — never introspected (EC7)
	}
	client, err := store.authentikClient(ctx)
	if err != nil {
		return false, err // genuine store/decrypt fault — fail closed via 500 (G1)
	}
	if client == nil {
		return false, nil // authentik mode active but not configured — fail closed via 401
	}
	active, err := client.Introspect(ctx, token)
	if err != nil {
		return false, nil // transport/timeout/non-2xx — fail closed via 401 (G5), not 500: Authentik being unreachable isn't a local store fault
	}
	return active, nil // active==false → (false, nil), same effect: 401
}

// BearerToken extracts the token from an "Authorization: Bearer <token>"
// header, matching the "Bearer" scheme case-insensitively per RFC 7235 §2.1
// (auth-scheme names are case-insensitive) — plain strings.TrimPrefix only
// matched an exact-case "Bearer " and would fail closed (deny, not a
// security bug) on a lowercase "bearer" scheme some clients send. Returns
// "" (never introspected) if the header is absent or doesn't use the
// Bearer scheme.
func BearerToken(r *http.Request) string {
	const scheme = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return ""
	}
	return strings.TrimSpace(h[len(scheme):])
}
