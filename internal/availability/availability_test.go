package availability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/prowlarr"
	"github.com/curtiswtaylorjr/sakms/internal/tmdb"
)

// fakeTMDB serves /movie/{id}, /tv/{id}/external_ids etc. from a handler the
// test supplies — status controls whether the details call fails.
func fakeTMDB(t *testing.T, handler http.HandlerFunc) *tmdb.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return tmdb.New(tmdb.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

// fakeProwlarr serves /api/v1/search — releasesJSON is the raw array body, or
// the server 500s if fail is true (to exercise the Prowlarr-error path).
func fakeProwlarr(t *testing.T, releasesJSON string, fail bool) *prowlarr.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(releasesJSON))
	}))
	t.Cleanup(srv.Close)
	return prowlarr.New(prowlarr.Config{BaseURL: srv.URL, APIKey: "test-key"}, srv.Client())
}

func assertCheckedAt(t *testing.T, got string) {
	t.Helper()
	if got == "" {
		t.Errorf("expected a non-empty CheckedAt timestamp")
		return
	}
	if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
		t.Errorf("CheckedAt %q is not RFC3339Nano-parseable: %v", got, err)
	}
}

const oneRelease = `[{"guid":"1","title":"Some.Movie.2023.1080p.WEB-DL","indexer":"I","protocol":"torrent","downloadUrl":"http://x/1"}]`
const noReleases = `[]`
const movieDetails = `{"id":42,"title":"Some Movie","imdb_id":"tt1234567","runtime":100}`
const externalIDs = `{"tvdb_id":789}`

func TestCheckMovie(t *testing.T) {
	tests := []struct {
		name          string
		tmdb          *tmdb.Client
		prowlarr      *prowlarr.Client
		wantErr       bool
		wantAvailable bool
		wantCount     int
	}{
		{
			name:          "available",
			tmdb:          fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(movieDetails)) }),
			prowlarr:      fakeProwlarr(t, oneRelease, false),
			wantAvailable: true,
			wantCount:     1,
		},
		{
			name:          "not available",
			tmdb:          fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(movieDetails)) }),
			prowlarr:      fakeProwlarr(t, noReleases, false),
			wantAvailable: false,
			wantCount:     0,
		},
		{
			name:     "tmdb error",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusInternalServerError) }),
			prowlarr: fakeProwlarr(t, oneRelease, false),
			wantErr:  true,
		},
		{
			name:     "prowlarr error",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(movieDetails)) }),
			prowlarr: fakeProwlarr(t, "", true),
			wantErr:  true,
		},
		{
			name:     "nil tmdb client",
			tmdb:     nil,
			prowlarr: fakeProwlarr(t, oneRelease, false),
			wantErr:  true,
		},
		{
			name:     "nil prowlarr client",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(movieDetails)) }),
			prowlarr: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := CheckMovie(context.Background(), tt.tmdb, tt.prowlarr, 42)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none (result %+v)", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Available != tt.wantAvailable || res.ReleaseCount != tt.wantCount {
				t.Errorf("got available=%v count=%d, want available=%v count=%d", res.Available, res.ReleaseCount, tt.wantAvailable, tt.wantCount)
			}
			assertCheckedAt(t, res.CheckedAt)
		})
	}
}

// TestCheckMovie_PassesIMDBID proves the movie's IMDB id flows from
// MovieDetails into the Prowlarr query (the whole point of the details lookup:
// a precise id-scoped probe), with the "tt" prefix stripped by SearchByID.
func TestCheckMovie_PassesIMDBID(t *testing.T) {
	var gotIMDB, gotTMDB, gotType, gotCats string
	tmdbClient := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(movieDetails)) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIMDB = r.URL.Query().Get("imdbid")
		gotTMDB = r.URL.Query().Get("tmdbid")
		gotType = r.URL.Query().Get("type")
		gotCats = r.URL.Query().Get("categories")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(oneRelease))
	}))
	t.Cleanup(srv.Close)
	prowlarrClient := prowlarr.New(prowlarr.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())

	if _, err := CheckMovie(context.Background(), tmdbClient, prowlarrClient, 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotIMDB != "1234567" { // "tt" stripped by SearchByID
		t.Errorf("expected imdbid=1234567 (tt stripped), got %q", gotIMDB)
	}
	if gotTMDB != "42" {
		t.Errorf("expected tmdbid=42, got %q", gotTMDB)
	}
	if gotType != "movie" {
		t.Errorf("expected type=movie, got %q", gotType)
	}
	if gotCats != "2000" {
		t.Errorf("expected categories=2000, got %q", gotCats)
	}
}

func TestCheckSeries(t *testing.T) {
	tests := []struct {
		name          string
		tmdb          *tmdb.Client
		prowlarr      *prowlarr.Client
		wantErr       bool
		wantAvailable bool
		wantCount     int
	}{
		{
			name:          "available",
			tmdb:          fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(externalIDs)) }),
			prowlarr:      fakeProwlarr(t, oneRelease, false),
			wantAvailable: true,
			wantCount:     1,
		},
		{
			name:          "not available",
			tmdb:          fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(externalIDs)) }),
			prowlarr:      fakeProwlarr(t, noReleases, false),
			wantAvailable: false,
			wantCount:     0,
		},
		{
			name:     "tmdb error",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusInternalServerError) }),
			prowlarr: fakeProwlarr(t, oneRelease, false),
			wantErr:  true,
		},
		{
			name:     "prowlarr error",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(externalIDs)) }),
			prowlarr: fakeProwlarr(t, "", true),
			wantErr:  true,
		},
		{
			name:     "nil tmdb client",
			tmdb:     nil,
			prowlarr: fakeProwlarr(t, oneRelease, false),
			wantErr:  true,
		},
		{
			name:     "nil prowlarr client",
			tmdb:     fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(externalIDs)) }),
			prowlarr: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := CheckSeries(context.Background(), tt.tmdb, tt.prowlarr, 100, 0, 0)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none (result %+v)", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Available != tt.wantAvailable || res.ReleaseCount != tt.wantCount {
				t.Errorf("got available=%v count=%d, want available=%v count=%d", res.Available, res.ReleaseCount, tt.wantAvailable, tt.wantCount)
			}
			assertCheckedAt(t, res.CheckedAt)
		})
	}
}

// TestCheckSeries_DegenerateQueryShortCircuits proves that when TMDB has no
// TVDB id on file (ExternalIDs returns 0) and no season/episode was given, the
// probe reports unavailable WITHOUT hitting Prowlarr — an id-less tvsearch
// would be a meaningless noise query.
func TestCheckSeries_DegenerateQueryShortCircuits(t *testing.T) {
	tmdbClient := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"tvdb_id":0}`)) })
	var prowlarrHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prowlarrHit = true
		w.Write([]byte(oneRelease))
	}))
	t.Cleanup(srv.Close)
	prowlarrClient := prowlarr.New(prowlarr.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())

	res, err := CheckSeries(context.Background(), tmdbClient, prowlarrClient, 100, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prowlarrHit {
		t.Errorf("expected no Prowlarr call for a degenerate (tvdb=0, no season/episode) probe")
	}
	if res.Available || res.ReleaseCount != 0 {
		t.Errorf("expected unavailable/0, got %+v", res)
	}
	assertCheckedAt(t, res.CheckedAt)
}

// TestCheckSeries_PassesTVDBIDAndSeasonEpisode proves the resolved TVDB id and
// the season/episode scope flow into the Prowlarr tvsearch query.
func TestCheckSeries_PassesTVDBIDAndSeasonEpisode(t *testing.T) {
	var gotTVDB, gotSeason, gotEp, gotType, gotCats string
	tmdbClient := fakeTMDB(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(externalIDs)) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTVDB = r.URL.Query().Get("tvdbid")
		gotSeason = r.URL.Query().Get("season")
		gotEp = r.URL.Query().Get("ep")
		gotType = r.URL.Query().Get("type")
		gotCats = r.URL.Query().Get("categories")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(oneRelease))
	}))
	t.Cleanup(srv.Close)
	prowlarrClient := prowlarr.New(prowlarr.Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())

	if _, err := CheckSeries(context.Background(), tmdbClient, prowlarrClient, 100, 3, 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTVDB != "789" {
		t.Errorf("expected tvdbid=789, got %q", gotTVDB)
	}
	if gotSeason != "3" || gotEp != "5" {
		t.Errorf("expected season=3 ep=5, got season=%q ep=%q", gotSeason, gotEp)
	}
	if gotType != "tvsearch" {
		t.Errorf("expected type=tvsearch, got %q", gotType)
	}
	if gotCats != "5000" {
		t.Errorf("expected categories=5000, got %q", gotCats)
	}
}
