package rename

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
	"github.com/curtiswtaylorjr/sakms/internal/stashapi"
	"github.com/curtiswtaylorjr/sakms/internal/stashbox"
	"github.com/curtiswtaylorjr/sakms/internal/throttle"
)

// countingAI counts ChatJSON calls and always returns resp — lets tests
// assert whether the legacy AI/text pipeline actually ran.
type countingAI struct {
	calls int
	resp  map[string]any
}

func (a *countingAI) ChatJSON(ctx context.Context, prompt string) (map[string]any, error) {
	a.calls++
	return a.resp, nil
}

// fakeHasher stands in for the videophash hasher: a canned hash per path, or a
// forced error for paths in errs (proving per-file fail-open to the legacy
// pipeline). Satisfies the rename-local PHasher interface.
type fakeHasher struct {
	hashes map[string]string
	errs   map[string]bool
}

func (f *fakeHasher) Hash(_ context.Context, path string) (string, error) {
	if f.errs[path] {
		return "", fmt.Errorf("boom hashing %s", path)
	}
	return f.hashes[path], nil
}

// fakeProber stands in for the mediainfo prober, supplying a canned duration
// (seconds) per path — the source of a proposal's DurationSeconds now that it
// no longer rides in on a Stash read. Satisfies the rename-local Prober interface.
type fakeProber struct {
	durations map[string]float64
}

func (f *fakeProber) Probe(_ context.Context, path string) (*mediainfo.Probe, error) {
	return &mediainfo.Probe{Duration: f.durations[path]}, nil
}

// sceneJSON renders a StashFile fixture into the raw shape Stash's own
// findScenes query returns, for fakeStash below.
func sceneJSON(path string, f *stashapi.StashFile) map[string]any {
	fps := []map[string]any{}
	if f.PHash != "" {
		fps = append(fps, map[string]any{"type": "phash", "value": f.PHash})
	}
	return map[string]any{
		"id": f.SceneID, "title": f.Title, "date": f.Date,
		"studio":    map[string]any{"name": f.Studio},
		"stash_ids": []any{},
		"files": []map[string]any{{
			"path": path, "width": f.Width, "height": f.Height, "duration": f.Duration,
			"video_codec": f.VideoCodec, "bit_rate": f.BitRate, "fingerprints": fps,
		}},
	}
}

// fakeStash stands in for a local Stash instance's FindSceneInfoByPath(s) — no
// longer used by Scan (identification computes its own phash now), but still
// exercised by rename_test.go's SubmitFingerprintRetry tests, which re-read a
// current phash/duration off a live Stash. Kept here for those callers.
type fakeStash struct {
	t         *testing.T
	files     map[string]*stashapi.StashFile
	failLoad  bool
	scanCalls [][]string
	onScan    func(paths []string)
}

func newFakeStash(t *testing.T, f *fakeStash) *stashapi.Client {
	t.Helper()
	f.t = t
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return stashapi.New(stashapi.Config{URL: srv.URL, APIKey: "k"}, srv.Client())
}

func (f *fakeStash) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string                     `json:"query"`
		Variables map[string]json.RawMessage `json:"variables"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.Contains(req.Query, "ScanPaths"):
		var input struct {
			Paths []string `json:"paths"`
		}
		json.Unmarshal(req.Variables["input"], &input)
		f.scanCalls = append(f.scanCalls, input.Paths)
		if f.onScan != nil {
			f.onScan(input.Paths)
		}
		fmt.Fprint(w, `{"data":{"metadataScan":"job1"}}`)
	case strings.Contains(req.Query, "FindJob"):
		fmt.Fprint(w, `{"data":{"findJob":{"status":"FINISHED"}}}`)
	case f.failLoad:
		fmt.Fprint(w, `{"errors":[{"message":"stash unreachable"}]}`)
	case strings.Contains(req.Query, "BatchFindByPath"):
		data := map[string]any{}
		for key, raw := range req.Variables {
			if !strings.HasPrefix(key, "p") {
				continue
			}
			var path string
			json.Unmarshal(raw, &path)
			scenes := []any{}
			if file := f.files[path]; file != nil {
				scenes = append(scenes, sceneJSON(path, file))
			}
			data["s"+strings.TrimPrefix(key, "p")] = map[string]any{"scenes": scenes}
		}
		body, _ := json.Marshal(map[string]any{"data": data})
		w.Write(body)
	case strings.Contains(req.Query, "FindByPath("):
		var path string
		json.Unmarshal(req.Variables["path"], &path)
		scenes := []any{}
		if file := f.files[path]; file != nil {
			scenes = append(scenes, sceneJSON(path, file))
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"findScenes": map[string]any{"scenes": scenes}}})
		w.Write(body)
	default:
		f.t.Fatalf("unexpected stash query: %s", req.Query)
	}
}

// giveBackRecord captures what a fake stash-box saw at fingerprint give-back,
// so the give-back-through-Apply test can assert the duration actually flowed
// (the guard against the silent duration-coupling regression).
type giveBackRecord struct {
	submitted bool
	sceneID   string
	hash      string
	duration  int
}

// newFakeAdultBox stands in for one stash-box's fingerprint endpoints. It
// serves BOTH the cascade lookup (findScenesBySceneFingerprints, keyed by phash
// — a missing key means no match) AND, when rec is non-nil, the give-back
// submitFingerprint mutation, recording the submitted duration. Reimplemented
// here (rather than shared with internal/identify's own fingerprint test fake)
// since that one is unexported to its own package.
func newFakeAdultBox(t *testing.T, results map[string]struct{ id, title string }, rec *giveBackRecord) *stashbox.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "SubmitFingerprint") {
			var v struct {
				Input struct {
					SceneID     string `json:"scene_id"`
					Fingerprint struct {
						Hash     string `json:"hash"`
						Duration int    `json:"duration"`
					} `json:"fingerprint"`
				} `json:"input"`
			}
			json.Unmarshal(req.Variables, &v)
			if rec != nil {
				rec.submitted = true
				rec.sceneID = v.Input.SceneID
				rec.hash = v.Input.Fingerprint.Hash
				rec.duration = v.Input.Fingerprint.Duration
			}
			fmt.Fprint(w, `{"data":{"submitFingerprint":true}}`)
			return
		}

		var v struct {
			FPs [][]map[string]string `json:"fps"`
		}
		json.Unmarshal(req.Variables, &v)
		matches := make([][]map[string]any, len(v.FPs))
		for i, fp := range v.FPs {
			hash := fp[0]["hash"]
			if scene, ok := results[hash]; ok {
				matches[i] = []map[string]any{{"id": scene.id, "title": scene.title, "release_date": "", "studio": map[string]any{"name": ""}}}
			} else {
				matches[i] = []map[string]any{}
			}
		}
		body, _ := json.Marshal(map[string]any{"data": map[string]any{"findScenesBySceneFingerprints": matches}})
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return stashbox.New(stashbox.Config{Endpoint: srv.URL, APIKey: "k", HasVoteField: true}, srv.Client())
}

// adultTestSession builds a Whisparr *mode.Session wired for the phash-first
// pipeline. The fake Servarr handler fails the test if it's ever called —
// scanAdultPhashFirst and its legacy fallback (proposeOneAdult) never touch
// the *arr app; that's Apply's job, not Scan's.
func adultTestSession(t *testing.T, stash *stashapi.Client, ai *countingAI, boxes map[string]*stashbox.Client) *mode.Session {
	t.Helper()
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("must never call the *arr app during Scan, got %s %s", r.Method, r.URL.Path)
	})
	sess.Stash = stash
	var aiClient identify.AIClient
	if ai != nil {
		aiClient = ai
	}
	sess.Identify = &identify.Identifier{
		AI:       aiClient,
		GiveBack: identify.NewGiveBack(boxes),
		Throttle: throttle.New(0),
	}
	return sess
}

func TestScanAdultPhashFirst_CascadeHit_SkipsAIEntirely(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{path: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, nil)
	ai := &countingAI{}
	sess := adultTestSession(t, nil, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(out), out)
	}
	p := out[0]
	if p.Status != proposals.Pending || p.Title != "Cascade Scene" || p.ForeignID != "box-scene-1" {
		t.Fatalf("expected a fingerprint-cascade hit, got %+v", p)
	}
	if p.GiveBackBox != "stashdb" || p.GiveBackSceneID != "box-scene-1" {
		t.Errorf("expected give-back target captured from the cascade match, got box=%q scene=%q", p.GiveBackBox, p.GiveBackSceneID)
	}
	if p.PHash != "hash1" || p.DurationSeconds != 1800 {
		t.Errorf("expected phash from the hasher and duration from the prober, got phash=%q duration=%d", p.PHash, p.DurationSeconds)
	}
	if ai.calls != 0 {
		t.Errorf("expected the AI/text pipeline to never run on a cascade hit, got %d calls", ai.calls)
	}
}

func TestScanAdultPhashFirst_CascadeMiss_FallsThroughToProposeOneAdult(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{}
	stashdb := newFakeAdultBox(t, nil, nil) // no match anywhere
	ai := &countingAI{resp: map[string]any{"studio": nil, "title": nil, "year": nil, "performers": nil}}
	sess := adultTestSession(t, nil, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 proposal, got %d: %+v", len(out), out)
	}
	if out[0].Status != proposals.Unmatched {
		t.Fatalf("expected a cascade miss to fall through to the legacy pipeline and end up Unmatched, got %+v", out[0])
	}
	if ai.calls == 0 {
		t.Error("expected the legacy AI/text pipeline to actually run on a cascade miss")
	}
}

// TestScanAdultPhashFirst_HashError_PerFileFallsOpenToLegacy proves the
// fail-open is per-file, not all-or-nothing: candidate A hashes and matches the
// cascade (Pending), while candidate B's Hash errors and routes ONLY B to the
// legacy pipeline. Replaces the old Stash-load-error test — a batched Stash
// read (and its all-or-nothing failure mode) no longer exists.
func TestScanAdultPhashFirst_HashError_PerFileFallsOpenToLegacy(t *testing.T) {
	pathA := "/media/Adult/a.mp4"
	pathB := "/media/Adult/b.mp4"
	hasher := &fakeHasher{
		hashes: map[string]string{pathA: "hashA"},
		errs:   map[string]bool{pathB: true},
	}
	prober := &fakeProber{durations: map[string]float64{pathA: 1800}}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hashA": {id: "box-a", title: "Scene A"},
	}, nil)
	ai := &countingAI{resp: map[string]any{"studio": nil, "title": nil, "year": nil, "performers": nil}}
	sess := adultTestSession(t, nil, ai, map[string]*stashbox.Client{"stashdb": stashdb})

	candidates := []adultCandidate{
		{root: servarr.RootFolder{Path: "/media/Adult"}, uf: servarr.UnmappedFolder{Name: "a.mp4", Path: pathA}},
		{root: servarr.RootFolder{Path: "/media/Adult"}, uf: servarr.UnmappedFolder{Name: "b.mp4", Path: pathB}},
	}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 2 {
		t.Fatalf("expected 2 proposals (one cascade hit, one legacy), got %d: %+v", len(out), out)
	}
	// Order-preserved build: cascade hits first, then legacy fallbacks.
	if out[0].Status != proposals.Pending || out[0].Title != "Scene A" || out[0].PHash != "hashA" {
		t.Errorf("expected candidate A to resolve via the cascade despite B erroring, got %+v", out[0])
	}
	if out[1].SourcePath != pathB || out[1].Status != proposals.Unmatched {
		t.Errorf("expected candidate B (hash error) to fall through to the legacy pipeline, got %+v", out[1])
	}
	if ai.calls != 1 {
		t.Errorf("expected the legacy pipeline to run for exactly the one errored candidate, got %d AI calls", ai.calls)
	}
}

// TestScanAdultPhashFirst_GiveBackFiresWithProberDuration is the NON-NEGOTIABLE
// guard against the silent duration-coupling regression: it carries a
// cascade-hit proposal all the way through rename.Apply and asserts that
// fingerprint give-back actually FIRED with the prober-sourced duration. The
// stamping check in the cascade-hit test alone does NOT catch this, because
// submitFingerprintGiveBack fails open (returns false, no error) on a
// non-positive DurationSeconds — so a duration that never made it in would look
// identical to a give-back that simply wasn't configured.
func TestScanAdultPhashFirst_GiveBackFiresWithProberDuration(t *testing.T) {
	path := "/media/Adult/scene1.mp4"
	rec := &giveBackRecord{}
	stashdb := newFakeAdultBox(t, map[string]struct{ id, title string }{
		"hash1": {id: "box-scene-1", title: "Cascade Scene"},
	}, rec)
	hasher := &fakeHasher{hashes: map[string]string{path: "hash1"}}
	prober := &fakeProber{durations: map[string]float64{path: 1800}}

	// A session whose Whisparr accepts Apply's Add + downloaded-scan (Scan never
	// touches the *arr app; Apply does), and whose give-back routes to the
	// recording box.
	sess := newTestSession(t, servarr.Whisparr, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie" && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case r.URL.Path == "/api/v3/command":
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected *arr call during Apply: %s %s", r.Method, r.URL.Path)
		}
	})
	sess.Identify = &identify.Identifier{
		GiveBack: identify.NewGiveBack(map[string]*stashbox.Client{"stashdb": stashdb}),
		Throttle: throttle.New(0),
	}

	candidates := []adultCandidate{{
		root: servarr.RootFolder{Path: "/media/Adult"},
		uf:   servarr.UnmappedFolder{Name: "scene1.mp4", Path: path},
	}}
	out := scanAdultPhashFirst(context.Background(), sess, hasher, prober, candidates, nil, []servarr.QualityProfile{{ID: 4}})
	if len(out) != 1 || out[0].Status != proposals.Pending {
		t.Fatalf("expected one Pending cascade-hit proposal to carry into Apply, got %+v", out)
	}
	if out[0].PHash != "hash1" || out[0].DurationSeconds != 1800 {
		t.Fatalf("expected the scanned proposal to carry phash+prober duration into Apply, got phash=%q duration=%d", out[0].PHash, out[0].DurationSeconds)
	}

	_, submitted, err := Apply(context.Background(), sess, out[0])
	if err != nil {
		t.Fatalf("Apply returned an error: %v", err)
	}
	if !submitted {
		t.Fatal("expected Apply to report fingerprint give-back as submitted")
	}
	if !rec.submitted {
		t.Fatal("expected the stash-box to have received a give-back submission end-to-end through Apply")
	}
	if rec.duration != 1800 {
		t.Errorf("expected give-back to carry the prober's duration 1800 (the duration-coupling guard), got %d", rec.duration)
	}
	if rec.hash != "hash1" || rec.sceneID != "box-scene-1" {
		t.Errorf("expected give-back to carry the scanned phash/scene, got hash=%q scene=%q", rec.hash, rec.sceneID)
	}
}
