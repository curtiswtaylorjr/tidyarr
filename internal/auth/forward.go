package auth

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

const (
	forwardSecretHashKey   = "auth_forward_secret_hash"   // hex SHA-256, show-once
	forwardUserHeaderKey   = "auth_forward_user_header"   // default "Remote-User"
	forwardSecretHeaderKey = "auth_forward_secret_header" // default "X-Proxy-Secret"
)

// defaultForwardUserHeader/defaultForwardSecretHeader are the header names
// ForwardHeaders falls back to when an operator hasn't customized them —
// sane out-of-the-box values for a reverse proxy to forward.
const (
	defaultForwardUserHeader   = "Remote-User"
	defaultForwardSecretHeader = "X-Proxy-Secret"
)

// SetForwardSecret persists raw's hash as the active forward-mode shared
// secret, replacing whatever was there before — the single write path
// shared by GenerateForwardSecret's fresh-mint branch and the first-run
// setup handler's "operator supplied their own secret" branch (§2.2b),
// mirroring persistKey's role for the API key.
func (s *Store) SetForwardSecret(ctx context.Context, raw string) error {
	return s.settings.Set(ctx, forwardSecretHashKey, hex.EncodeToString(hashKey(raw)))
}

// GenerateForwardSecret mints a fresh shared secret (reusing newRandomKey,
// the same entropy source as the API key), persists its hash, and returns
// the raw value ONCE (G6) — mirrors Regenerate's shape for the API key.
// There is no getter that ever returns the raw secret again; "reveal once"
// is enforced by simply never storing it anywhere but this return value.
func (s *Store) GenerateForwardSecret(ctx context.Context) (raw string, err error) {
	raw, err = newRandomKey()
	if err != nil {
		return "", err
	}
	if err := s.SetForwardSecret(ctx, raw); err != nil {
		return "", err
	}
	return raw, nil
}

// ForwardHeaders returns the configured identity/secret header names,
// defaulting to Remote-User/X-Proxy-Secret when an operator hasn't set
// either explicitly.
func (s *Store) ForwardHeaders(ctx context.Context) (userHeader, secretHeader string, err error) {
	userHeader, err = s.settings.Get(ctx, forwardUserHeaderKey)
	if errors.Is(err, settings.ErrNotFound) {
		userHeader = defaultForwardUserHeader
	} else if err != nil {
		return "", "", err
	}
	secretHeader, err = s.settings.Get(ctx, forwardSecretHeaderKey)
	if errors.Is(err, settings.ErrNotFound) {
		secretHeader = defaultForwardSecretHeader
	} else if err != nil {
		return "", "", err
	}
	return userHeader, secretHeader, nil
}

// SetForwardHeaders persists custom identity/secret header names —
// used by the post-first-run Settings-switch config endpoint (§2.3), not
// by first-run setup (which leaves both at their ForwardHeaders defaults).
func (s *Store) SetForwardHeaders(ctx context.Context, userHeader, secretHeader string) error {
	if err := s.settings.Set(ctx, forwardUserHeaderKey, userHeader); err != nil {
		return err
	}
	return s.settings.Set(ctx, forwardSecretHeaderKey, secretHeader)
}

// ForwardConfigured reports whether a forward-mode shared secret has been
// set — the G4 precondition for switching INTO "forward" mode.
func (s *Store) ForwardConfigured(ctx context.Context) (bool, error) {
	_, err := s.settings.Get(ctx, forwardSecretHashKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// VerifyForwardSecret constant-time-compares presented against the active
// forward secret's hash. Guardrail-critical order, exact mirror of
// VerifyAPIKey (apikey.go) — do not reorder:
//  1. presented is trimmed and an empty result is treated as absent, not
//     compared — otherwise an unconfigured store (want == nil) could let
//     subtle.ConstantTimeCompare("", "") == 1 false-pass an empty presented
//     secret through as a "match" (Edge Case #7).
//  2. the stored hash is looked up; a genuine store error is returned to
//     the caller (which must fail closed — see auth.ForwardAuth) rather
//     than treated as "no secret".
//  3. "not configured" short-circuits to false — a presented secret must
//     never verify against nothing.
//  4. only then is the constant-time comparison performed (G3), and it is
//     mandatory: never replace this with a plain == comparison.
func (s *Store) VerifyForwardSecret(ctx context.Context, presented string) (bool, error) {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return false, nil
	}
	hexHash, err := s.settings.Get(ctx, forwardSecretHashKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want, err := hex.DecodeString(hexHash)
	if err != nil {
		return false, err
	}
	got := hashKey(presented)
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
