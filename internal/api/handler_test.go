package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConnectionsTestHandler_EndToEnd exercises the real path a Settings
// "Test connection" click takes: an HTTP POST into Tidyarr's own server,
// which itself makes a real HTTP call out to the configured service (here, a
// second httptest server standing in for a live Radarr) and reports back
// over JSON. This is the thing actually wiring identify/servarr/ollama/
// stashapi into cmd/tidyarr is meant to prove works, not just that each
// package compiles in isolation.
func TestConnectionsTestHandler_EndToEnd(t *testing.T) {
	fakeRadarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":123}]`))
	}))
	defer fakeRadarr.Close()

	tidyarrSrv := httptest.NewServer(NewMux(testHTTPClient()))
	defer tidyarrSrv.Close()

	reqBody, _ := json.Marshal(ConnectionTestRequest{
		Service: "radarr", URL: fakeRadarr.URL, APIKey: "test-key",
	})
	resp, err := http.Post(tidyarrSrv.URL+"/api/connections/test", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result ConnectionTestResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Fatalf("expected ok=true, got %+v", result)
	}
}

func TestConnectionsTestHandler_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(NewMux(testHTTPClient()))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/connections/test", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed body, got %d", resp.StatusCode)
	}
}
