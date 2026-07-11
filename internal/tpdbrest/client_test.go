package tpdbrest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSearchByHash_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("hash") != "abc123" || q.Get("hash_type") != "phash" {
			t.Errorf("unexpected query params: %v", q)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer testkey" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"A Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "A Scene" || out[0].Site != "Some Site" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearchByTitle_OmitsSiteWhenEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "Some Title" {
			t.Errorf("expected q=Some Title, got %q", q.Get("q"))
		}
		if _, has := q["site"]; has {
			t.Errorf("expected no 'site' param when studio is empty, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSearchByTitle_IncludesSiteWhenSet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("site") != "Tushy" {
			t.Errorf("expected site=Tushy, got %q", q.Get("site"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.SearchByTitle(context.Background(), "Some Title", "Tushy"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "1" {
			t.Errorf("expected per_page=1, got %q", q.Get("per_page"))
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a ping, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_UnauthorizedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error for a bad key")
	}
}

func TestGet_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "badkey", &http.Client{Timeout: 5 * time.Second})
	_, err := c.SearchByHash(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error on non-200 status")
	}
}

func TestGet_EmptySiteFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Site Scene","date":"2024-01-01","site":null}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Site != "" {
		t.Fatalf("expected empty site for null site, got %q", out[0].Site)
	}
}

func TestSearchPerformers_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/performers" {
			t.Errorf("expected path /performers, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "riley reid" {
			t.Errorf("expected q=riley reid, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"p1","name":"Riley Reid"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchPerformers(context.Background(), "riley reid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Riley Reid" || out[0].ID != "p1" {
		t.Fatalf("got %+v", out)
	}
}

func TestSearchSites_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sites" {
			t.Errorf("expected path /sites, got %q", r.URL.Path)
		}
		if r.URL.Query().Get("q") != "tushy" {
			t.Errorf("expected q=tushy, got %q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"s1","name":"Tushy"}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchSites(context.Background(), "tushy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Name != "Tushy" || out[0].ID != "s1" {
		t.Fatalf("got %+v", out)
	}
}
