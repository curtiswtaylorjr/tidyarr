package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/connections"
	"github.com/curtiswtaylorjr/sakms/internal/tpdbrest"
)

// adultScene is the Discover response shape for one TPDB scene — a stable,
// lowercase-json DTO mirroring search.go's searchResult, so the frontend reads
// item.id/item.title/item.studio/item.date the same way it reads TMDB's
// lowercase-tagged tmdb.Item. tpdbrest.Scene itself carries NO json tags, so
// encoding it raw would emit capitalized keys (ID/Title/Site/Date) the frontend
// doesn't read — hence this explicit mapping (note Site → studio).
//
// Image is the scene thumbnail URL (TPDB's flat "image" field, served from
// cdn.theporndb.net — already covered by internal/imageproxy's allowlist). It
// is often empty (many scenes carry no art), so the frontend must render a
// text-only card when it's blank and route non-empty values through the image
// proxy, never hot-link TPDB directly (plan Decision #7).
type adultScene struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Studio string `json:"studio"`
	Date   string `json:"date"`
	Image  string `json:"image"`
}

// adultDiscoverHandler backs Adult's Discover screen against ThePornDB's REST
// catalog — plain paginated browse (page/perPage) when no q is given, or a
// title text-search when q is set (the "browse + search-by-term for v1" the
// plan's Stage 7 calls for; both reuse tpdbrest's existing get/doGet plumbing,
// SearchByTitle being the search-by-term entry point). Adult-only: its identity
// space is a stash-box/TPDB scene, not a TMDB id, so it can't share
// discoverHandler's TMDB path — this route is registered on the concrete
// /api/modes/adult/discover pattern (a literal "adult", not a {mode} wildcard),
// which ServeMux prefers over the wildcard {mode}/discover one for Adult.
//
// It builds a standalone tpdbrest client from the "tpdb" connection rather than
// going through mode.Build — mode.Session doesn't expose the raw REST client
// (it's wrapped inside sess.Identify), and this handler needs nothing else a
// session carries. Same construction mode.go's buildIdentifier uses.
func adultDiscoverHandler(httpClient *http.Client, connStore *connections.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		conn, err := connStore.Get(ctx, "tpdb")
		if err != nil {
			if errors.Is(err, connections.ErrNotFound) {
				http.Error(w, "tpdb isn't configured yet — add it in Settings first", http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		client := tpdbrest.New(conn.URL, conn.APIKey, httpClient)

		var scenes []tpdbrest.Scene
		if q := r.URL.Query().Get("q"); q != "" {
			// Search-by-term entry point — no studio narrowing here (the browse
			// screen searches by free title text; identify.SearchTPDB is what
			// narrows by studio during identification).
			scenes, err = client.SearchByTitle(ctx, q, "")
		} else {
			// Plain paginated browse — BrowseScenes clamps bad/blank values.
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
			scenes, err = client.BrowseScenes(ctx, page, perPage)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		out := make([]adultScene, len(scenes))
		for i, s := range scenes {
			out[i] = adultScene{ID: s.ID, Title: s.Title, Studio: s.Site, Date: s.Date, Image: s.Image}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}
