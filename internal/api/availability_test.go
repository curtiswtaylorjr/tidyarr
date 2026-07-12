package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/availability"
)

// fakeTMDBServer serves the TMDB endpoints availability needs: /movie/{id}
// (with a top-level imdb_id) and /tv/{id}/external_ids (tvdb_id).
func fakeTMDBServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/movie/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":42,"title":"Some Movie","imdb_id":"tt1234567"}`))
	})
	mux.HandleFunc("/tv/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tvdb_id":789}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAvailabilityHandler_Movies(t *testing.T) {
	tmdbSrv := fakeTMDBServer(t)
	prowlarr := fakeProwlarr(t, `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL","indexer":"I","protocol":"torrent","downloadUrl":"http://x/1"}]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/availability?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result availability.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.Available || result.ReleaseCount != 1 {
		t.Errorf("expected available with 1 release, got %+v", result)
	}
	if result.CheckedAt == "" {
		t.Errorf("expected a CheckedAt timestamp, got empty")
	}
}

func TestAvailabilityHandler_Series(t *testing.T) {
	tmdbSrv := fakeTMDBServer(t)
	prowlarr := fakeProwlarr(t, `[]`)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	if err := connStore.Upsert(ctx, "tmdb", tmdbSrv.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "prowlarr", prowlarr.URL, "key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/series/availability?tmdbId=100&season=3&episode=5")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result availability.Result
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Available || result.ReleaseCount != 0 {
		t.Errorf("expected unavailable with 0 releases, got %+v", result)
	}
}

func TestAvailabilityHandler_AdultRejected(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/adult/availability?tmdbId=42")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for adult, got %d", resp.StatusCode)
	}
}

func TestAvailabilityHandler_MissingTMDBID(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/modes/movies/availability")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without a tmdbId param, got %d", resp.StatusCode)
	}
}
