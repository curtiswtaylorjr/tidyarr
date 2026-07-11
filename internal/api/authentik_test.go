package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// TestAuthentikGet_NeverReturnsSecret covers G6: the authentik-mode status
// endpoint must report whether a secret is configured without ever
// including the secret's own value anywhere in the response.
func TestAuthentikGet_NeverReturnsSecret(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()
	cipher, err := secretStore.Encrypt("the-super-secret-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthentikConfig(ctx, "https://sso.example.com", "the-client-id", cipher); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthentikMux(authStore, secretStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/authentik")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(rawBody), "the-super-secret-client-secret") {
		t.Fatalf("authentik status response must never contain the raw client secret, got %s", rawBody)
	}
	if strings.Contains(string(rawBody), cipher) {
		t.Fatalf("authentik status response must never contain the encrypted client secret either, got %s", rawBody)
	}

	var status authentikStatusResponse
	if err := json.Unmarshal(rawBody, &status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !status.HasSecret {
		t.Error("expected hasSecret=true once a secret is configured")
	}
	if status.URL != "https://sso.example.com" || status.ClientID != "the-client-id" {
		t.Errorf("expected url/clientId to round-trip, got %+v", status)
	}
}

func TestAuthentikGet_NoSecretConfigured(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	srv := httptest.NewServer(NewAuthentikMux(authStore, secretStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/authentik")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var status authentikStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	if status.HasSecret {
		t.Error("expected hasSecret=false on a fresh store")
	}
}

// TestAuthentikPut_UpdatesStoredConfigEncrypted proves the PUT config
// endpoint encrypts the client secret before it ever reaches settings.Set —
// the raw secret sent in the request body must not appear anywhere in the
// underlying settings store.
func TestAuthentikPut_UpdatesStoredConfigEncrypted(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	srv := httptest.NewServer(NewAuthentikMux(authStore, secretStore))
	defer srv.Close()

	body, _ := json.Marshal(authentikConfigRequest{
		URL: "https://sso.example.com", ClientID: "cid", ClientSecret: "raw-secret-value",
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/authentik", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	url, clientID, cipher, err := authStore.AuthentikConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://sso.example.com" || clientID != "cid" {
		t.Errorf("expected url/clientId to persist, got (%q, %q)", url, clientID)
	}
	if cipher == "raw-secret-value" {
		t.Fatal("expected the client secret to be stored encrypted, not plaintext")
	}
	decrypted, err := secretStore.Decrypt(cipher)
	if err != nil {
		t.Fatalf("unexpected error decrypting stored secret: %v", err)
	}
	if decrypted != "raw-secret-value" {
		t.Errorf("expected the stored ciphertext to decrypt back to the original secret, got %q", decrypted)
	}
}

func TestAuthentikPut_RequiresAllFields(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	srv := httptest.NewServer(NewAuthentikMux(authStore, secretStore))
	defer srv.Close()

	body, _ := json.Marshal(authentikConfigRequest{URL: "https://sso.example.com"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/authentik", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 with missing clientId/clientSecret, got %d", resp.StatusCode)
	}
}

// TestPutMode_AuthentikWithoutCreds_400 covers G4: switching INTO authentik
// mode must be refused when no url/client id/client secret has been
// configured yet.
func TestPutMode_AuthentikWithoutCreds_400(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeAuthentik})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 switching to authentik without configured credentials, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModePassword {
		t.Errorf("expected the rejected switch to leave mode unchanged (%q), got %q", auth.ModePassword, mode)
	}
}

func TestPutMode_AuthentikWithCreds_204(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()
	cipher, err := secretStore.Encrypt("the-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthentikConfig(ctx, "https://sso.example.com", "cid", cipher); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeAuthentik})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 switching to authentik with configured credentials, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModeAuthentik {
		t.Errorf("expected mode %q, got %q", auth.ModeAuthentik, mode)
	}
}

// TestSetup_AuthentikWritesConfigAndMode is the first-run bootstrap's
// end-to-end proof (plan §0.7/§3.3b), mirroring slice 2's
// TestSetup_ForwardGeneratesSecretAndWritesMode: POST /api/auth/setup with
// mode:"authentik" and NO prior credential must succeed through the PUBLIC
// setup endpoint, persist the config, write auth_mode atomically, and NEVER
// echo the client secret back in the response (G6 — unlike forward mode,
// the operator already has their own copy from Authentik's own UI).
func TestSetup_AuthentikWritesConfigAndMode(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	configuredBefore, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configuredBefore {
		t.Fatal("expected a fresh instance to be unconfigured before setup")
	}

	body, _ := json.Marshal(authCredentialsRequest{
		Mode:                  "authentik",
		AuthentikURL:          "https://sso.example.com",
		AuthentikClientID:     "the-client-id",
		AuthentikClientSecret: "the-client-secret-value",
	})
	resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("expected no session cookie for authentik mode, got %+v", resp.Cookies())
	}
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(rawBody), "the-client-secret-value") {
		t.Fatalf("expected the client secret to never be echoed back in the setup response, got %s", rawBody)
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModeAuthentik {
		t.Errorf("expected auth_mode to be written as %q, got %q", auth.ModeAuthentik, mode)
	}
	configuredAfter, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !configuredAfter {
		t.Fatal("expected the instance to report Configured=true after authentik-mode setup")
	}

	url, clientID, cipher, err := authStore.AuthentikConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "https://sso.example.com" || clientID != "the-client-id" {
		t.Errorf("expected url/clientId to persist, got (%q, %q)", url, clientID)
	}
	decrypted, err := tokenEnc.Decrypt(cipher)
	if err != nil {
		t.Fatalf("unexpected error decrypting stored secret: %v", err)
	}
	if decrypted != "the-client-secret-value" {
		t.Errorf("expected the persisted secret to decrypt back to what was submitted, got %q", decrypted)
	}
}

func TestSetup_AuthentikMissingFields_400(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	for _, body := range []authCredentialsRequest{
		{Mode: "authentik"},
		{Mode: "authentik", AuthentikURL: "https://sso.example.com"},
		{Mode: "authentik", AuthentikURL: "https://sso.example.com", AuthentikClientID: "cid"},
	} {
		raw, _ := json.Marshal(body)
		resp, err := http.Post(srv.URL+"/api/auth/setup", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for incomplete authentik setup body %+v, got %d", body, resp.StatusCode)
		}
	}

	configured, err := authStore.Configured(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configured {
		t.Error("expected all rejected authentik setup attempts to leave the instance unconfigured")
	}
}

// TestStatus_AuthentikMode_PresenceOnly_NeverIntrospects is the load-bearing
// proof for this whole slice's amplification-avoidance fix (plan §3.3's
// critic-driven fix). A fake introspection server is injected via the
// stored config, but /api/auth/status must NEVER call it, even when a
// garbage (non-empty) bearer token is presented — the status endpoint's
// "authenticated" field for authentik mode is presence-only. If this test
// fails, it means the status handler is calling the real, network-bound
// AuthentikAuth from a public, unauthenticated, attacker-rate-controlled
// endpoint — a real amplification vector against Authentik itself.
func TestStatus_AuthentikMode_PresenceOnly_NeverIntrospects(t *testing.T) {
	authStore, secretStore := testAuthStore(t)
	ctx := context.Background()

	var introspectionCalls int
	fakeIntrospect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		introspectionCalls++
		t.Error("the public /api/auth/status endpoint must NEVER call the introspection endpoint (amplification vector) — this fake server received a request")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active": true}`))
	}))
	defer fakeIntrospect.Close()

	cipher, err := secretStore.Encrypt("the-client-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthentikConfig(ctx, fakeIntrospect.URL, "the-client-id", cipher); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthMode(ctx, auth.ModeAuthentik); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthMux(authStore, secretStore))
	defer srv.Close()

	// A garbage (non-empty) bearer token: presence-only heuristic reports
	// authenticated:true WITHOUT ever validating the token against
	// Authentik.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/status", nil)
	req.Header.Set("Authorization", "Bearer this-token-is-complete-garbage-and-invalid")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status authStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Mode != auth.ModeAuthentik {
		t.Errorf("expected mode %q, got %q", auth.ModeAuthentik, status.Mode)
	}
	if !status.Authenticated {
		t.Error("expected authenticated=true on bearer presence alone (presence-only heuristic)")
	}

	// No bearer at all: authenticated:false, still zero introspection calls.
	resp2, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status2 authStatusResponse
	json.NewDecoder(resp2.Body).Decode(&status2)
	resp2.Body.Close()
	if status2.Authenticated {
		t.Error("expected authenticated=false with no bearer header presented")
	}

	if introspectionCalls != 0 {
		t.Fatalf("expected ZERO introspection calls from the status endpoint, got %d", introspectionCalls)
	}
}
