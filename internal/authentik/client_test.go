package authentik

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIntrospect_ActiveTrue(t *testing.T) {
	var gotPath string
	var gotForm map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		gotForm = map[string][]string(r.PostForm)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("expected form-urlencoded content type, got %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active": true}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, ClientID: "cid", ClientSecret: "csecret"}, srv.Client())
	active, err := c.Introspect(context.Background(), "sometoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !active {
		t.Error("expected active=true")
	}
	if gotPath != "/application/o/introspect/" {
		t.Errorf("expected path %q, got %q", "/application/o/introspect/", gotPath)
	}
	if gotForm["token"][0] != "sometoken" {
		t.Errorf("expected token form param %q, got %v", "sometoken", gotForm["token"])
	}
	if gotForm["client_id"][0] != "cid" {
		t.Errorf("expected client_id form param %q, got %v", "cid", gotForm["client_id"])
	}
	if gotForm["client_secret"][0] != "csecret" {
		t.Errorf("expected client_secret form param %q, got %v", "csecret", gotForm["client_secret"])
	}
}

func TestIntrospect_ActiveFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active": false}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, ClientID: "cid", ClientSecret: "csecret"}, srv.Client())
	active, err := c.Introspect(context.Background(), "sometoken")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if active {
		t.Error("expected active=false")
	}
}

func TestIntrospect_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, ClientID: "cid", ClientSecret: "csecret"}, srv.Client())
	active, err := c.Introspect(context.Background(), "sometoken")
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if active {
		t.Error("expected active=false alongside the error")
	}
}

func TestIntrospect_TransportError(t *testing.T) {
	// A URL with no listener at all — the request fails at the transport
	// layer, before any HTTP status is even received.
	c := New(Config{URL: "http://127.0.0.1:1", ClientID: "cid", ClientSecret: "csecret"}, http.DefaultClient)
	active, err := c.Introspect(context.Background(), "sometoken")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if active {
		t.Error("expected active=false alongside the error")
	}
}

func TestIntrospect_URLTrailingSlashTolerated(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active": true}`))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + "/", ClientID: "cid", ClientSecret: "csecret"}, srv.Client())
	if _, err := c.Introspect(context.Background(), "sometoken"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/application/o/introspect/" {
		t.Errorf("expected path %q regardless of trailing slash on Config.URL, got %q", "/application/o/introspect/", gotPath)
	}
}
