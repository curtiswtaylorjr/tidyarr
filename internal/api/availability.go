package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/availability"
	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// availabilityHandler answers "does a release exist for this picked title?" for
// one Discover card — a lightweight, id-based indexer probe (see
// internal/availability), not a grab and nothing staged or persisted, the same
// read-only-proxy posture as discoverHandler/resolveTVDBIDHandler. It's a
// deferred per-item call by design (fired only when a card is rendered), the
// same "don't compute this for a whole trending list eagerly" pattern
// resolveTVDBIDHandler follows.
//
// The identity a check keys on differs by mode, and that difference is real,
// not a bug: Movies/Series probe by a TMDB id (tmdbId query param, + optional
// season/episode Series scoping); Adult has NO tmdb/imdb/tvdb id — its identity
// is a stash-box/TPDB scene (see identify.MatchResult) and its releases aren't
// id-indexed — so Adult takes studio+title query params and a free-text probe
// (see availability.CheckAdultScene) instead. Every mode needs Prowlarr; only
// Movies/Series additionally need TMDB. A missing tmdbId (Movies/Series) or a
// missing title (Adult) is a 400.
func availabilityHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		ctx := r.Context()

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		var result availability.Result
		if m == mode.Adult {
			// Adult's identity shape: studio+title, not a tmdbId (see the doc
			// above). title is required; studio is optional (it only narrows the
			// free-text query).
			title := r.URL.Query().Get("title")
			if title == "" {
				http.Error(w, "title query parameter is required for adult availability", http.StatusBadRequest)
				return
			}
			studio := r.URL.Query().Get("studio")
			result, err = availability.CheckAdultScene(ctx, sess.Prowlarr, studio, title)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
			return
		}

		// Movies/Series: an id-based probe.
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil {
			http.Error(w, "tmdbId query parameter is required and must be an integer", http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		if m == mode.Series {
			// Optional Series scoping — a bad/blank value is treated as "not
			// scoped" (0), same tolerant parse the frontend's search picker uses.
			season, _ := strconv.Atoi(r.URL.Query().Get("season"))
			episode, _ := strconv.Atoi(r.URL.Query().Get("episode"))
			result, err = availability.CheckSeries(ctx, sess.TMDB, sess.Prowlarr, tmdbID, season, episode)
		} else {
			result, err = availability.CheckMovie(ctx, sess.TMDB, sess.Prowlarr, tmdbID)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}
