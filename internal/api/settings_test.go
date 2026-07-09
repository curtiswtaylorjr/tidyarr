package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ollamaModelEndpoints is a single-entry table (kept as a table, not a bare
// constant, so each test case gets its own t.Run subtest name) for the one
// shared Ollama model setting — Adult identification and Movies/Series
// Rename's AI fallback both read mode.OllamaModelKey via this one endpoint.
var ollamaModelEndpoints = []struct {
	name string
	path string
}{
	{"shared", "/api/settings/ollama-model"},
}

// TestOllamaModel_RoundTrip drives the real mux: GET on a blank install
// returns an empty model, PUT stores it, and a follow-up GET reads it back.
func TestOllamaModel_RoundTrip(t *testing.T) {
	for _, ep := range ollamaModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
			defer srv.Close()

			// Unset is a normal state: GET returns 200 with an empty model.
			resp, err := http.Get(srv.URL + ep.path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			var got ollamaModelResponse
			json.NewDecoder(resp.Body).Decode(&got)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
			}
			if got.Model != "" {
				t.Errorf("expected empty model before anything is set, got %q", got.Model)
			}

			// PUT stores it.
			body, _ := json.Marshal(ollamaModelRequest{Model: "qwen2.5vl:7b"})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader(body))
			putResp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			putResp.Body.Close()
			if putResp.StatusCode != http.StatusNoContent {
				t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
			}

			// GET reads it back.
			resp2, err := http.Get(srv.URL + ep.path)
			if err != nil {
				t.Fatalf("GET failed: %v", err)
			}
			defer resp2.Body.Close()
			var got2 ollamaModelResponse
			json.NewDecoder(resp2.Body).Decode(&got2)
			if got2.Model != "qwen2.5vl:7b" {
				t.Errorf("expected the stored model to round-trip, got %q", got2.Model)
			}
		})
	}
}

// TestOllamaModel_EmptyModelRejected confirms a PUT with an empty model is a
// 400 — the endpoint won't store a blank value.
func TestOllamaModel_EmptyModelRejected(t *testing.T) {
	for _, ep := range ollamaModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
			defer srv.Close()

			body, _ := json.Marshal(ollamaModelRequest{Model: ""})
			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader(body))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for an empty model, got %d", resp.StatusCode)
			}
		})
	}
}

// TestOllamaModel_InvalidBody confirms a malformed JSON body is a 400.
func TestOllamaModel_InvalidBody(t *testing.T) {
	for _, ep := range ollamaModelEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			connStore, propStore, allowStore, settingsStore := testStores(t)
			srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodPut, srv.URL+ep.path, bytes.NewReader([]byte("not json")))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT failed: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
			}
		})
	}
}
