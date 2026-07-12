package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNetscanHostHandler_RejectsPublicIP proves the ProbeHost SSRF guardrail
// surfaces as a 400 at the HTTP layer for a public IP.
func TestNetscanHostHandler_RejectsPublicIP(t *testing.T) {
	h := netscanHostHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodPost, "/api/netscan/host", strings.NewReader(`{"host":"8.8.8.8"}`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a public IP, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestNetscanKnownHandler_ReturnsJSONArray proves the known-hosts route always
// returns a JSON array (never null), even when nothing is found.
func TestNetscanKnownHandler_ReturnsJSONArray(t *testing.T) {
	h := netscanKnownHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodGet, "/api/netscan/known", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var findings []struct {
		Service string `json:"service"`
		URL     string `json:"url"`
		Label   string `json:"label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &findings); err != nil {
		t.Fatalf("response is not a JSON array: %v (%s)", err, rec.Body.String())
	}
	// The general probe response must never carry a credential field.
	if strings.Contains(strings.ToLower(rec.Body.String()), "apikey") {
		t.Fatalf("known-hosts response contains an apiKey-shaped field: %s", rec.Body.String())
	}
}

// TestNetscanProwlarrKeyHandler_ReturnsKey proves the dedicated, explicit
// key-fetch route returns the key from a Prowlarr /initialize.json.
func TestNetscanProwlarrKeyHandler_ReturnsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/initialize.json" {
			w.Write([]byte(`{"instanceName":"Prowlarr","apiKey":"LIVEKEY123"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	h := netscanProwlarrKeyHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodPost, "/api/netscan/prowlarr-key", strings.NewReader(`{"url":"`+srv.URL+`"}`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if out.APIKey != "LIVEKEY123" {
		t.Fatalf("expected the live key, got %q", out.APIKey)
	}
}

func TestNetscanProwlarrKeyHandler_MissingURL(t *testing.T) {
	h := netscanProwlarrKeyHandler(testHTTPClient())
	req := httptest.NewRequest(http.MethodPost, "/api/netscan/prowlarr-key", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing url, got %d", rec.Code)
	}
}
