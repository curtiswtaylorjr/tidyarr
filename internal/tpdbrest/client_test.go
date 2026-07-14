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

// TestSearchByHash_ToleratesNumericSceneID regression-covers a real production
// error: TPDB returns a bare JSON number for _id on some scenes, not always a
// quoted string, which the plain-string rawScene.ID field used to reject
// outright ("json: cannot unmarshal number into Go struct field
// rawScene.data._id of type string"). flexID must decode either shape.
func TestSearchByHash_ToleratesNumericSceneID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":42,"title":"Numeric ID Scene","date":"2024-03-03","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].ID != "42" || out[0].Title != "Numeric ID Scene" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_PaginatesWithoutSearchTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "3" {
			t.Errorf("expected per_page=10 page=3, got %v", q)
		}
		if _, hasQ := q["q"]; hasQ {
			t.Errorf("expected no search term on a browse, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"s9","title":"Browsed Scene","date":"2024-02-02","site":{"name":"BrowseSite"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.BrowseScenes(context.Background(), 3, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Title != "Browsed Scene" || out[0].Site != "BrowseSite" {
		t.Fatalf("got %+v", out)
	}
}

func TestBrowseScenes_ClampsBadPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("per_page") != "20" || q.Get("page") != "1" {
			t.Errorf("expected defaulted per_page=20 page=1, got %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.BrowseScenes(context.Background(), 0, -5); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestGet_ParsesDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"Timed Scene","date":"2024-01-01","site":{"name":"Some Site"},"duration":1800}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0].Duration != 1800 {
		t.Fatalf("expected Duration=1800 seconds, got %+v", out)
	}
}

func TestGet_MissingDurationIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"_id":"1","title":"No Duration Scene","date":"2024-01-01","site":{"name":"Some Site"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "testkey", &http.Client{Timeout: 5 * time.Second})
	out, err := c.SearchByHash(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// A scene with no "duration" in the response must decode to 0, the
	// documented "unknown, skip the bitrate check" sentinel — never an error.
	if len(out) != 1 || out[0].Duration != 0 {
		t.Fatalf("expected Duration=0 when absent from response, got %+v", out)
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
