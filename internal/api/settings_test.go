package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdultOllamaModel_RoundTrip drives the real mux: GET on a blank install
// returns an empty model, PUT stores it, and a follow-up GET reads it back.
func TestAdultOllamaModel_RoundTrip(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	// Unset is a normal state: GET returns 200 with an empty model.
	resp, err := http.Get(srv.URL + "/api/settings/adult-ollama-model")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	var got adultOllamaModelResponse
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on unset GET, got %d", resp.StatusCode)
	}
	if got.Model != "" {
		t.Errorf("expected empty model before anything is set, got %q", got.Model)
	}

	// PUT stores it.
	body, _ := json.Marshal(adultOllamaModelRequest{Model: "qwen2.5vl:7b"})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-ollama-model", bytes.NewReader(body))
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 on PUT, got %d", putResp.StatusCode)
	}

	// GET reads it back.
	resp2, err := http.Get(srv.URL + "/api/settings/adult-ollama-model")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp2.Body.Close()
	var got2 adultOllamaModelResponse
	json.NewDecoder(resp2.Body).Decode(&got2)
	if got2.Model != "qwen2.5vl:7b" {
		t.Errorf("expected the stored model to round-trip, got %q", got2.Model)
	}
}

// TestAdultOllamaModel_EmptyModelRejected confirms a PUT with an empty model
// is a 400 — the endpoint won't store a blank value.
func TestAdultOllamaModel_EmptyModelRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	body, _ := json.Marshal(adultOllamaModelRequest{Model: ""})
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-ollama-model", bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty model, got %d", resp.StatusCode)
	}
}

// TestAdultOllamaModel_InvalidBody confirms a malformed JSON body is a 400.
func TestAdultOllamaModel_InvalidBody(t *testing.T) {
	connStore, propStore, allowStore, settingsStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), settingsStore))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/settings/adult-ollama-model", bytes.NewReader([]byte("not json")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
