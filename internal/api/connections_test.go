package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func TestTestConnection_Radarr_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/rootfolder" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("missing X-Api-Key header")
		}
		w.Write([]byte(`[{"id":1,"path":"/media/Movies","accessible":true,"freeSpace":123}]`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "radarr", URL: srv.URL, APIKey: "test-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Sonarr_WrongKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "sonarr", URL: srv.URL, APIKey: "wrong-key",
	})
	if result.OK {
		t.Fatal("expected failure on 401")
	}
	if result.Error == "" {
		t.Error("expected a populated error message")
	}
}

func TestTestConnection_Ollama_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"models":[{"name":"qwen2.5:14b"}]}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "ollama", URL: srv.URL,
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Ollama_Unreachable(t *testing.T) {
	// A closed server: connection refused, not a status-code failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "ollama", URL: srv.URL,
	})
	if result.OK {
		t.Fatal("expected failure against a closed server")
	}
}

func TestTestConnection_Stash_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiKey") != "stash-key" {
			t.Error("missing ApiKey header")
		}
		w.Write([]byte(`{"data":{"allTags":[{"id":"1","name":"low-quality-flag"}]}}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "stash", URL: srv.URL, APIKey: "stash-key",
	})
	if !result.OK || result.Error != "" {
		t.Fatalf("expected success, got %+v", result)
	}
}

func TestTestConnection_Stash_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"not authorized"}]}`))
	}))
	defer srv.Close()

	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "stash", URL: srv.URL, APIKey: "bad-key",
	})
	if result.OK {
		t.Fatal("expected failure on a GraphQL error response")
	}
}

func TestTestConnection_UnsupportedService(t *testing.T) {
	result := TestConnection(context.Background(), testHTTPClient(), ConnectionTestRequest{
		Service: "plex", URL: "http://example.com",
	})
	if result.OK {
		t.Fatal("expected failure for an unsupported service")
	}
	if result.Error == "" {
		t.Error("expected a populated error message naming the bad service")
	}
}
