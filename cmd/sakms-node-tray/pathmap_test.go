package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// --- pure display logic ----------------------------------------------------

func TestBuildKeyRows(t *testing.T) {
	catalog := []string{"movies_library_root_folder", "series_library_root_folder", "adult_library_root_folder"}
	authored := []authoredMapping{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies"},
		{Key: "series_library_root_folder", NodePath: ""}, // blank = skip, treated as unset
	}
	got := buildKeyRows(catalog, authored, nil)
	want := []keyRow{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true, HasAuthoredKey: true},
		{Key: "series_library_root_folder", NodePath: "", Mapped: false},
		{Key: "adult_library_root_folder", NodePath: "", Mapped: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows = %+v, want %+v", got, want)
	}
}

func TestBuildKeyRows_PreservesCatalogOrderAndIgnoresUnknownAuthored(t *testing.T) {
	catalog := []string{"b_key", "a_key"}
	authored := []authoredMapping{
		{Key: "a_key", NodePath: "/a"},
		{Key: "ghost_key", NodePath: "/ghost"}, // not in catalog → not rendered
	}
	got := buildKeyRows(catalog, authored, nil)
	want := []keyRow{
		{Key: "b_key", NodePath: "", Mapped: false},
		{Key: "a_key", NodePath: "/a", Mapped: true, HasAuthoredKey: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows = %+v, want %+v", got, want)
	}
}

// TestEscapeMenuLabel asserts the mnemonic-escape doubles every literal
// underscore. The DBusMenu/GTK rendering side that consumes a single "_" as a
// mnemonic (and a doubled "__" as one literal underscore) cannot be exercised
// from a Go unit test; see escapeMenuLabel's doc comment for the cited
// convention. This proves the neutralization: the on-wire label carries "__"
// wherever the source had "_", which GTK renders back as a single "_".
func TestEscapeMenuLabel(t *testing.T) {
	cases := map[string]string{
		"movies_library_root_folder": "movies__library__root__folder",
		"/mnt/My_Show":               "/mnt/My__Show",
		"no-underscores-here":        "no-underscores-here",
		"":                           "",
		"_":                          "__",
		"a__b":                       "a____b", // already-doubled input doubles again; escaping is not idempotent, by design
	}
	for in, want := range cases {
		if got := escapeMenuLabel(in); got != want {
			t.Errorf("escapeMenuLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestKeyDisplayLabel_KnownKeys proves all 5 known catalog keys render as their
// human-friendly label (none of which contain an underscore to mangle).
func TestKeyDisplayLabel_KnownKeys(t *testing.T) {
	cases := map[string]string{
		"movies_library_root_folder": "Movies",
		"series_library_root_folder": "Series",
		"adult_library_root_folder":  "Adult",
		"movies_kids_root_path":      "Movies (Kids)",
		"series_kids_root_path":      "Series (Kids)",
	}
	for key, want := range cases {
		if got := keyDisplayLabel(key); got != want {
			t.Errorf("keyDisplayLabel(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestKeyDisplayLabel_UnknownKeyFallsBackToEscapedRaw proves an unrecognized
// catalog key (a future addition) falls back to the mnemonic-escaped raw key
// rather than crashing or rendering blank.
func TestKeyDisplayLabel_UnknownKeyFallsBackToEscapedRaw(t *testing.T) {
	if got := keyDisplayLabel("future_unknown_key"); got != "future__unknown__key" {
		t.Errorf("unknown key = %q, want escaped raw %q", got, "future__unknown__key")
	}
	if got := keyDisplayLabel(""); got != "" {
		t.Errorf("empty key = %q, want %q (no crash, no blank-label panic)", got, "")
	}
}

// TestBuildKeyRows_LegacyPathMapOnly is the primary regression test for Bug 2:
// a key that has a live PathMap (Remap) entry but NO AuthoredPaths record — the
// exact shape of a mapping set via the OLD server-side operator UI — must render
// as Mapped=true with the PathMap's real Local path, not "not set". Before the
// fix, buildKeyRows consulted only AuthoredPaths and reported these as unset.
func TestBuildKeyRows_LegacyPathMapOnly(t *testing.T) {
	catalog := []string{"movies_library_root_folder", "series_library_root_folder", "adult_library_root_folder"}
	authored := []authoredMapping(nil) // node never authored these — legacy operator mappings
	pathMap := []remapEntry{
		{Server: "/srv/movies", Local: "/mnt/movies", Key: "movies_library_root_folder"},
		{Server: "/srv/series", Local: "/mnt/series", Key: "series_library_root_folder"},
		{Server: "/srv/adult", Local: "/mnt/adult", Key: "adult_library_root_folder"},
	}
	got := buildKeyRows(catalog, authored, pathMap)
	want := []keyRow{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true},
		{Key: "series_library_root_folder", NodePath: "/mnt/series", Mapped: true},
		{Key: "adult_library_root_folder", NodePath: "/mnt/adult", Mapped: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows (legacy PathMap-only) = %+v, want %+v", got, want)
	}
}

// TestBuildKeyRows_PendingAuthoredFallback proves the pending case still works:
// a key with an AuthoredPaths record but no matching PathMap entry yet (the set
// was pushed but the server hasn't echoed it back into the Remap table) still
// renders as mapped, using the authored value.
func TestBuildKeyRows_PendingAuthoredFallback(t *testing.T) {
	catalog := []string{"movies_library_root_folder"}
	authored := []authoredMapping{{Key: "movies_library_root_folder", NodePath: "/mnt/just-picked"}}
	pathMap := []remapEntry(nil) // server hasn't echoed the Remap pair back yet
	got := buildKeyRows(catalog, authored, pathMap)
	want := []keyRow{{Key: "movies_library_root_folder", NodePath: "/mnt/just-picked", Mapped: true, HasAuthoredKey: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows (pending authored fallback) = %+v, want %+v", got, want)
	}
}

// TestBuildKeyRows_LivePathMapWinsOverAuthored proves precedence: when both a
// live PathMap entry and an AuthoredPaths record exist for a key and disagree
// (a re-pick whose new value the server hasn't echoed yet), the PathMap Local —
// what is actually in effect for dispatch right now — wins.
func TestBuildKeyRows_LivePathMapWinsOverAuthored(t *testing.T) {
	catalog := []string{"movies_library_root_folder"}
	authored := []authoredMapping{{Key: "movies_library_root_folder", NodePath: "/mnt/new-repick"}}
	pathMap := []remapEntry{{Server: "/srv/movies", Local: "/mnt/live-in-effect", Key: "movies_library_root_folder"}}
	got := buildKeyRows(catalog, authored, pathMap)
	want := []keyRow{{Key: "movies_library_root_folder", NodePath: "/mnt/live-in-effect", Mapped: true, HasAuthoredKey: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows (live wins) = %+v, want %+v", got, want)
	}
}

// TestBuildKeyRows_UnkeyedPathMapIgnored proves a Remap entry with no Key (a
// pre-Key-field entry, or one whose Local is blank) does not spuriously mark a
// key mapped — matching by Key requires a real Key AND a non-blank Local.
func TestBuildKeyRows_UnkeyedPathMapIgnored(t *testing.T) {
	catalog := []string{"movies_library_root_folder"}
	pathMap := []remapEntry{
		{Server: "/srv/movies", Local: "/mnt/movies", Key: ""},               // no key → cannot correlate
		{Server: "/srv/other", Local: "", Key: "movies_library_root_folder"}, // blank local → treated unset
	}
	got := buildKeyRows(catalog, nil, pathMap)
	want := []keyRow{{Key: "movies_library_root_folder", NodePath: "", Mapped: false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildKeyRows (unkeyed/blank ignored) = %+v, want %+v", got, want)
	}
}

// TestKeyRowTitle_EscapesLabelAndPath proves the rendered title uses the
// friendly label and mnemonic-escapes an underscore-bearing node path.
func TestKeyRowTitle_EscapesLabelAndPath(t *testing.T) {
	got := keyRowTitle(keyRow{Key: "movies_library_root_folder", NodePath: "/mnt/My_Show", Mapped: true})
	if want := "Movies  →  /mnt/My__Show"; got != want {
		t.Errorf("keyRowTitle = %q, want %q", got, want)
	}
	// Unknown key: friendly label falls back to the escaped raw key.
	got = keyRowTitle(keyRow{Key: "future_key", Mapped: false})
	if want := "future__key  →  not set"; got != want {
		t.Errorf("keyRowTitle (unknown, unset) = %q, want %q", got, want)
	}
}

func TestKeyRowTitle(t *testing.T) {
	if got := keyRowTitle(keyRow{Key: "k", NodePath: "/x", Mapped: true}); got != "k  →  /x" {
		t.Errorf("mapped title = %q", got)
	}
	if got := keyRowTitle(keyRow{Key: "k", Mapped: false}); got != "k  →  not set" {
		t.Errorf("unset title = %q", got)
	}
}

func TestSetItemTitle(t *testing.T) {
	if got := setItemTitle(true); got != "Change folder…" {
		t.Errorf("mapped = %q", got)
	}
	if got := setItemTitle(false); got != "Set folder…" {
		t.Errorf("unset = %q", got)
	}
}

// TestRemoveItemVisible_LegacyVsAuthored proves the follow-up rule: a
// legacy-only row (Mapped via a live PathMap correlation, no AuthoredPaths
// record → HasAuthoredKey=false) hides its Remove control, while a node-authored
// row (HasAuthoredKey=true, whether or not PathMap has echoed back yet) keeps
// Remove available. An unmapped row never shows Remove.
func TestRemoveItemVisible_LegacyVsAuthored(t *testing.T) {
	cases := []struct {
		name string
		row  keyRow
		want bool
	}{
		{"legacy-only mapping hides Remove", keyRow{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true, HasAuthoredKey: false}, false},
		{"node-authored mapping shows Remove", keyRow{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true, HasAuthoredKey: true}, true},
		{"unmapped row hides Remove", keyRow{Key: "series_library_root_folder", Mapped: false}, false},
		{"unmapped-but-flagged is still hidden (needs Mapped too)", keyRow{Key: "series_library_root_folder", Mapped: false, HasAuthoredKey: true}, false},
	}
	for _, tc := range cases {
		if got := removeItemVisible(tc.row); got != tc.want {
			t.Errorf("%s: removeItemVisible = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBuildKeyRows_HasAuthoredKeyFlag proves buildKeyRows sets HasAuthoredKey to
// discriminate a legacy-only mapping (PathMap-correlated, no AuthoredPaths) from
// a node-authored one, driving removeItemVisible above end-to-end.
func TestBuildKeyRows_HasAuthoredKeyFlag(t *testing.T) {
	catalog := []string{"movies_library_root_folder", "series_library_root_folder"}
	authored := []authoredMapping{{Key: "series_library_root_folder", NodePath: "/mnt/series"}}
	pathMap := []remapEntry{
		{Server: "/srv/movies", Local: "/mnt/movies", Key: "movies_library_root_folder"}, // legacy: PathMap only
		{Server: "/srv/series", Local: "/mnt/series", Key: "series_library_root_folder"}, // node-authored: also in authored
	}
	got := buildKeyRows(catalog, authored, pathMap)
	want := []keyRow{
		{Key: "movies_library_root_folder", NodePath: "/mnt/movies", Mapped: true, HasAuthoredKey: false},
		{Key: "series_library_root_folder", NodePath: "/mnt/series", Mapped: true, HasAuthoredKey: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildKeyRows = %+v, want %+v", got, want)
	}
	if removeItemVisible(got[0]) {
		t.Error("legacy-only movies row must hide Remove")
	}
	if !removeItemVisible(got[1]) {
		t.Error("node-authored series row must keep Remove")
	}
}

func TestPathMappingGateOpen(t *testing.T) {
	if pathMappingGateOpen(0) {
		t.Error("gate should be CLOSED with zero media roots")
	}
	if !pathMappingGateOpen(1) {
		t.Error("gate should be OPEN with one media root")
	}
	if !pathMappingGateOpen(3) {
		t.Error("gate should be OPEN with three media roots")
	}
}

func TestPathPushWarningLine(t *testing.T) {
	// Empty error → hidden (the daemon clears lastPushError on a successful echo,
	// so the line disappears on its own).
	if text, show := pathPushWarningLine(""); show || text != "" {
		t.Errorf("empty error: got (%q, %v), want (\"\", false)", text, show)
	}
	text, show := pathPushWarningLine(`push for "movies_library_root_folder" failed: status 422`)
	if !show {
		t.Fatal("non-empty error should show the warning line")
	}
	if want := `⚠ Path mapping: last push failed — push for "movies_library_root_folder" failed: status 422 (re-pick a folder to retry)`; text != want {
		t.Errorf("warning text = %q, want %q", text, want)
	}
}

// --- control client round-trip over a real unix socket ---------------------

func TestPathMapControlClient_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "control.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /pathmap", func(w http.ResponseWriter, r *http.Request) {
		writePathMap(w, http.StatusOK, pathMapView{
			AuthoredPaths:   []authoredMapping{{Key: "movies_library_root_folder", NodePath: "/mnt/movies"}},
			LibraryPathKeys: []string{"movies_library_root_folder", "series_library_root_folder"},
			LastPushError:   "",
		})
	})
	mux.HandleFunc("POST /pathmap/set", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key       string `json:"key"`
			LocalPath string `json:"localPath"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.LocalPath == "/" {
			writePathMap(w, http.StatusBadRequest, pathMapView{Error: "path is too shallow"})
			return
		}
		// set echo omits the catalog, like the daemon's writePathMapState.
		writePathMap(w, http.StatusOK, pathMapView{
			AuthoredPaths: []authoredMapping{{Key: req.Key, NodePath: req.LocalPath}},
		})
	})
	mux.HandleFunc("POST /pathmap/clear", func(w http.ResponseWriter, r *http.Request) {
		writePathMap(w, http.StatusOK, pathMapView{AuthoredPaths: nil})
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })

	client := newControlClient(socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	view, err := client.getPathMap(ctx)
	if err != nil {
		t.Fatalf("getPathMap: %v", err)
	}
	if !reflect.DeepEqual(view.LibraryPathKeys, []string{"movies_library_root_folder", "series_library_root_folder"}) {
		t.Errorf("catalog = %v", view.LibraryPathKeys)
	}
	if len(view.AuthoredPaths) != 1 || view.AuthoredPaths[0].NodePath != "/mnt/movies" {
		t.Errorf("authored = %+v", view.AuthoredPaths)
	}

	view, err = client.setPathMap(ctx, "movies_library_root_folder", "/mnt/movies")
	if err != nil {
		t.Fatalf("setPathMap: %v", err)
	}
	if len(view.AuthoredPaths) != 1 || view.AuthoredPaths[0].Key != "movies_library_root_folder" {
		t.Errorf("set echo authored = %+v", view.AuthoredPaths)
	}

	if _, err = client.clearPathMap(ctx, "movies_library_root_folder"); err != nil {
		t.Fatalf("clearPathMap: %v", err)
	}

	// A daemon-side rejection (400 with an error body) surfaces as an error.
	_, err = client.setPathMap(ctx, "movies_library_root_folder", "/")
	if err == nil || err.Error() != "path is too shallow" {
		t.Fatalf("setPathMap(/) error = %v, want \"path is too shallow\"", err)
	}
}

func writePathMap(w http.ResponseWriter, status int, v pathMapView) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
