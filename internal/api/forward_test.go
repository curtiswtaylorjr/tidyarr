package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/auth"
)

// TestForwardGet_NeverReturnsSecret covers G6: the forward-mode status
// endpoint must report whether a secret is configured without ever
// including the secret's own value anywhere in the response.
func TestForwardGet_NeverReturnsSecret(t *testing.T) {
	authStore, _ := testAuthStore(t)
	ctx := context.Background()
	raw, err := authStore.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewForwardMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/forward")
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
	if strings.Contains(string(rawBody), raw) {
		t.Fatalf("forward status response must never contain the raw secret, got %s", rawBody)
	}

	var status forwardStatusResponse
	if err := json.Unmarshal(rawBody, &status); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !status.HasSecret {
		t.Error("expected hasSecret=true once a secret is configured")
	}
	if status.UserHeader == "" || status.SecretHeader == "" {
		t.Errorf("expected non-empty header names, got %+v", status)
	}
}

func TestForwardGet_NoSecretConfigured(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewForwardMux(authStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/auth/forward")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	var status forwardStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	if status.HasSecret {
		t.Error("expected hasSecret=false on a fresh store")
	}
}

// TestGenerateForwardSecretEndpoint_RevealOnce mirrors
// apikeyRegenerateHandler's reveal-once behavior for the forward-mode
// config endpoint.
func TestGenerateForwardSecretEndpoint_RevealOnce(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewForwardMux(authStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/auth/forward/secret", "application/json", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var secretResp forwardSecretResponse
	if err := json.NewDecoder(resp.Body).Decode(&secretResp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if secretResp.Secret == "" {
		t.Fatal("expected a non-empty generated secret")
	}

	ok, err := authStore.VerifyForwardSecret(context.Background(), secretResp.Secret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected the returned secret to verify against what was persisted")
	}
}

func TestForwardPutHeaders_UpdatesStoredHeaders(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewForwardMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(forwardHeadersRequest{UserHeader: "X-Custom-User", SecretHeader: "X-Custom-Secret"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/forward/headers", strings.NewReader(string(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	userHeader, secretHeader, err := authStore.ForwardHeaders(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userHeader != "X-Custom-User" || secretHeader != "X-Custom-Secret" {
		t.Errorf("expected custom headers to persist, got (%q, %q)", userHeader, secretHeader)
	}
}

// TestPutMode_ForwardWithoutSecret_400 covers G4: switching INTO forward
// mode must be refused when no shared secret has been configured yet.
func TestPutMode_ForwardWithoutSecret_400(t *testing.T) {
	authStore, _ := testAuthStore(t)
	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeForward})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", strings.NewReader(string(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 switching to forward without a configured secret, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModePassword {
		t.Errorf("expected the rejected switch to leave mode unchanged (%q), got %q", auth.ModePassword, mode)
	}
}

func TestPutMode_ForwardWithSecret_204(t *testing.T) {
	authStore, _ := testAuthStore(t)
	ctx := context.Background()
	if _, err := authStore.GenerateForwardSecret(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthModeMux(authStore))
	defer srv.Close()

	body, _ := json.Marshal(authModeRequest{Mode: auth.ModeForward})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/auth/mode", strings.NewReader(string(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 switching to forward with a configured secret, got %d", resp.StatusCode)
	}

	mode, err := authStore.AuthMode(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != auth.ModeForward {
		t.Errorf("expected mode %q, got %q", auth.ModeForward, mode)
	}
}

// TestStatus_ForwardMode_RealCheck proves forward mode's status
// "authenticated" field is a genuine forwardAuth result — NOT the
// presence-only heuristic slice 3 will use for authentik. The
// discriminating case is the middle one: a secret header that is PRESENT
// but WRONG. A presence-only heuristic (keyed on "is the header there at
// all") would report authenticated:true for that case; a real check (this
// one) must report false. The first and third cases (correct secret /
// absent secret) alone would pass under either implementation, so the
// wrong-secret case is the one that actually proves this isn't a
// heuristic.
func TestStatus_ForwardMode_RealCheck(t *testing.T) {
	authStore, tokenEnc := testAuthStore(t)
	ctx := context.Background()
	raw, err := authStore.GenerateForwardSecret(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := authStore.SetAuthMode(ctx, auth.ModeForward); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	userHeader, secretHeader, err := authStore.ForwardHeaders(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewAuthMux(authStore, tokenEnc))
	defer srv.Close()

	// Correct secret -> authenticated true.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/status", nil)
	req.Header.Set(secretHeader, raw)
	req.Header.Set(userHeader, "wade")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status authStatusResponse
	json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if status.Mode != auth.ModeForward {
		t.Errorf("expected mode %q, got %q", auth.ModeForward, status.Mode)
	}
	if !status.Authenticated {
		t.Error("expected authenticated=true with a correct forward secret")
	}

	// Secret header PRESENT but WRONG -> authenticated false. This is the
	// case that discriminates a real check from a presence-only heuristic.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/status", nil)
	req2.Header.Set(secretHeader, "definitely-the-wrong-secret")
	req2.Header.Set(userHeader, "wade")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status2 authStatusResponse
	json.NewDecoder(resp2.Body).Decode(&status2)
	resp2.Body.Close()
	if status2.Authenticated {
		t.Fatal("expected authenticated=false for a PRESENT but WRONG secret — a presence-only heuristic would wrongly report true here; forward mode's status check must be real")
	}

	// No secret header at all -> authenticated false.
	resp3, err := http.Get(srv.URL + "/api/auth/status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status3 authStatusResponse
	json.NewDecoder(resp3.Body).Decode(&status3)
	resp3.Body.Close()
	if status3.Authenticated {
		t.Error("expected authenticated=false with no secret header presented")
	}
}
