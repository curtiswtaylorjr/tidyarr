package auth

import (
	"context"
	"errors"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

const (
	authentikURLKey             = "auth_authentik_url"
	authentikClientIDKey        = "auth_authentik_client_id"
	authentikClientSecretEncKey = "auth_authentik_client_secret_enc" // ciphertext (secretStore.Encrypt), NOT hashed — this is an outbound credential SAK presents to Authentik, not a one-way local check like the password hash or forward secret hash.
)

// AuthentikConfig returns the stored Authentik config: the instance URL,
// the OAuth2 client id, and the client secret's CIPHERTEXT (never
// decrypted here — see AuthentikAuth in session.go, the only caller that
// ever needs the plaintext, via this Store's own enc field). All three
// fields are empty ("", "", "", nil) when nothing has been configured yet;
// a genuine settings-store error is returned as-is (fail closed per G1).
func (s *Store) AuthentikConfig(ctx context.Context) (url, clientID, clientSecretCipher string, err error) {
	url, err = s.settings.Get(ctx, authentikURLKey)
	if errors.Is(err, settings.ErrNotFound) {
		return "", "", "", nil
	} else if err != nil {
		return "", "", "", err
	}
	clientID, err = s.settings.Get(ctx, authentikClientIDKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", "", "", err
	}
	clientSecretCipher, err = s.settings.Get(ctx, authentikClientSecretEncKey)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", "", "", err
	}
	return url, clientID, clientSecretCipher, nil
}

// SetAuthentikConfig persists all three fields atomically-in-sequence,
// replacing whatever was there before. clientSecretCipher must already be
// encrypted (see internal/api's authSetupHandler/authentikPutHandler,
// which hold the secretStore this Store's own enc field decrypts with) —
// this Store never sees the plaintext at write time, only at
// AuthentikAuth's decrypt-to-introspect point.
func (s *Store) SetAuthentikConfig(ctx context.Context, url, clientID, clientSecretCipher string) error {
	if err := s.settings.Set(ctx, authentikURLKey, url); err != nil {
		return err
	}
	if err := s.settings.Set(ctx, authentikClientIDKey, clientID); err != nil {
		return err
	}
	return s.settings.Set(ctx, authentikClientSecretEncKey, clientSecretCipher)
}

// AuthentikConfigured reports whether Authentik config has been set — the
// G4 precondition for switching INTO "authentik" mode. Keyed off the
// client-secret ciphertext specifically (the last field SetAuthentikConfig
// writes), since all three fields are always written together in one call.
func (s *Store) AuthentikConfigured(ctx context.Context) (bool, error) {
	_, err := s.settings.Get(ctx, authentikClientSecretEncKey)
	if errors.Is(err, settings.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
