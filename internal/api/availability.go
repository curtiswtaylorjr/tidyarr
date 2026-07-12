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
// Movies/Series only: Adult's availability check is a later stage (its identity
// is a stash-box/TPDB scene, not a TMDB id, and its indexer probe is
// title-based), so Adult returns 400 here rather than a misleading movie
// result. season/episode are optional Series scoping; a missing/invalid tmdbId
// is a 400.
func availabilityHandler(httpClient *http.Client, connStore *connections.Store, settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m := mode.Mode(r.PathValue("mode"))
		if m == mode.Adult {
			http.Error(w, "availability checks aren't supported for adult yet", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdbId"))
		if err != nil {
			http.Error(w, "tmdbId query parameter is required and must be an integer", http.StatusBadRequest)
			return
		}

		sess, err := mode.Build(ctx, connStore, settingsStore, httpClient, m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if sess.TMDB == nil {
			http.Error(w, "tmdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}
		if sess.Prowlarr == nil {
			http.Error(w, "prowlarr isn't configured yet — add it in Settings first", http.StatusBadRequest)
			return
		}

		var result availability.Result
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
