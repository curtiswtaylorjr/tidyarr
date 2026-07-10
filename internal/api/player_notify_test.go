package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/library"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
)

// jellyfinUpdate mirrors internal/jellyfin.MediaUpdate's JSON shape — tests
// decode against this local copy rather than importing the client package's
// type, matching the wire-shape-assertion style internal/mode's own
// NotifyPlayers tests already use for the same reason (verify what's
// actually on the wire, not an internal Go value).
type jellyfinUpdate struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"`
}

// fakeJellyfin stands in for a live Jellyfin instance's targeted
// media-refresh endpoint, recording every /Library/Media/Updated POST's
// decoded batch (in call order) so a test can assert exactly what
// Session.NotifyPlayers sent, end to end through the real HTTP dispatch
// (applyProposalHandler -> applyByWorkflow -> NotifyPlayers -> this fake).
type fakeJellyfin struct {
	mu      sync.Mutex
	batches [][]jellyfinUpdate
	status  int // non-zero overrides the normal 204 response, for best-effort tests
}

func newFakeJellyfin(status int) *fakeJellyfin {
	return &fakeJellyfin{status: status}
}

func (f *fakeJellyfin) Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Library/Media/Updated":
			var body struct {
				Updates []jellyfinUpdate `json:"Updates"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decoding fake Jellyfin request body: %v", err)
			}
			f.mu.Lock()
			f.batches = append(f.batches, body.Updates)
			f.mu.Unlock()
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request to fake Jellyfin: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeJellyfin) Batches() [][]jellyfinUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]jellyfinUpdate, len(f.batches))
	copy(out, f.batches)
	return out
}

func (f *fakeJellyfin) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

// applyProposal POSTs /api/proposals/{id}/apply against srv, decoding and
// returning the updated proposal — the same request shape every apply test
// in this package already issues, factored out since this file has many.
func applyProposal(t *testing.T, srv *httptest.Server, id int64, body []byte) proposals.Proposal {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(id, 10)+"/apply", "application/json", reader)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from apply, got %d: %s", resp.StatusCode, respBody)
	}
	var applied proposals.Proposal
	if err := json.Unmarshal(respBody, &applied); err != nil {
		t.Fatalf("decoding apply response: %v", err)
	}
	return applied
}

// TestApplyProposalHandler_MoviesRename_NotifiesJellyfin proves row 1 of the
// player-rescan-notify call-site table end to end: a Movies Rename Apply
// reaches a real HTTP dispatch and notifies a fake Jellyfin with exactly
// {videoPath, Deleted} + {destPath, Created} — the resolved video file
// (here the file directly, not a wrapping directory) and the actual
// on-disk destination, not some precomputed guess.
func TestApplyProposalHandler_MoviesRename_NotifiesJellyfin(t *testing.T) {
	base := t.TempDir()
	sourcePath := filepath.Join(base, "incoming", "Movie.mkv")
	destRoot := filepath.Join(base, "Movies")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "Movie", SourcePath: sourcePath, RootFolderPath: destRoot, Title: "Some Movie", TMDBID: 453, Year: 2020},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applied := applyProposal(t, srv, saved[0].ID, nil)
	item, err := libStore.Get(ctx, int64(applied.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	want := []jellyfinUpdate{{Path: sourcePath, UpdateType: "Deleted"}, {Path: item.FilePath, UpdateType: "Created"}}
	if len(batch) != 2 || batch[0] != want[0] || batch[1] != want[1] {
		t.Errorf("expected Jellyfin batch %+v, got %+v", want, batch)
	}
}

// TestApplyProposalHandler_SeriesRename_NotifiesJellyfin is row 2's
// end-to-end counterpart — the Deleted side is p.SourcePath directly (no
// ResolveVideoFile indirection), the intentional asymmetry with row 1.
func TestApplyProposalHandler_SeriesRename_NotifiesJellyfin(t *testing.T) {
	base := t.TempDir()
	sourcePath := filepath.Join(base, "incoming", "Show.Name.S01E01.mkv")
	destRoot := filepath.Join(base, "TV")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Series, proposals.Rename, []proposals.Proposal{
		{
			Status: proposals.Pending, SourceName: "Show Name", SourcePath: sourcePath, RootFolderPath: destRoot,
			Title: "Show Name", TMDBID: 555, SeasonNumber: 1, EpisodeNumber: 1,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applied := applyProposal(t, srv, saved[0].ID, nil)
	series, err := libStore.GetSeriesByTMDBID(ctx, 555)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ep, err := libStore.GetEpisode(ctx, series.ID, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if int64(applied.TrackedID) != ep.ID {
		t.Fatalf("expected applied.TrackedID to be the episode id, got %d vs %d", applied.TrackedID, ep.ID)
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	want := []jellyfinUpdate{{Path: sourcePath, UpdateType: "Deleted"}, {Path: ep.FilePath, UpdateType: "Created"}}
	if len(batch) != 2 || batch[0] != want[0] || batch[1] != want[1] {
		t.Errorf("expected Jellyfin batch %+v, got %+v", want, batch)
	}
}

// TestApplyProposalHandler_MoviesRenameCollision_NotifiesActualUniquePath is
// Edge #4: when the naming-preset-computed destination is already occupied,
// RelocateMovie's place.UniquePath renames to a collision-suffixed path
// instead — notify must report THAT actual path, not the originally
// intended (and never-used) one.
func TestApplyProposalHandler_MoviesRenameCollision_NotifiesActualUniquePath(t *testing.T) {
	base := t.TempDir()
	destRoot := filepath.Join(base, "Movies")
	occupiedDir := filepath.Join(destRoot, "Some Movie (2020) [tmdbid-453]")
	if err := os.MkdirAll(occupiedDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	occupiedPath := filepath.Join(occupiedDir, "Some Movie (2020) [tmdbid-453].mkv")
	if err := os.WriteFile(occupiedPath, []byte("already here"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sourcePath := filepath.Join(base, "incoming", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("new copy"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Rename, []proposals.Proposal{
		{Status: proposals.Pending, SourceName: "Movie", SourcePath: sourcePath, RootFolderPath: destRoot, Title: "Some Movie", TMDBID: 453, Year: 2020},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applied := applyProposal(t, srv, saved[0].ID, nil)
	item, err := libStore.Get(ctx, int64(applied.TrackedID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.FilePath == occupiedPath {
		t.Fatalf("expected a collision-renamed unique path distinct from the already-occupied one, got %q", item.FilePath)
	}

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 2 || batch[1].Path != item.FilePath || batch[1].UpdateType != "Created" {
		t.Errorf("expected the Created entry to be the ACTUAL collision-renamed path %q, got %+v", item.FilePath, batch)
	}
}

// TestApplyProposalHandler_MoviesPurge_NotifiesJellyfin is row 4 end to end.
func TestApplyProposalHandler_MoviesPurge_NotifiesJellyfin(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TrackedID: int(item.ID)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 1 || batch[0].Path != filePath || batch[0].UpdateType != "Deleted" {
		t.Errorf("expected a single Deleted PathChange for %q, got %+v", filePath, batch)
	}
}

// TestApplyProposalHandler_SeriesPurge_NotifiesJellyfinNDeletes is row 5,
// Edge #2: N episode files removed in one Apply must reach Jellyfin as N
// Deleted entries in a single batch.
func TestApplyProposalHandler_SeriesPurge_NotifiesJellyfinNDeletes(t *testing.T) {
	dir := t.TempDir()
	ep1Path := filepath.Join(dir, "s01e01.mkv")
	ep2Path := filepath.Join(dir, "s01e02.mkv")
	if err := os.WriteFile(ep1Path, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(ep2Path, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: ep1Path}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 2, FilePath: ep2Path}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Series, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TrackedID: int(series.ID)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify CALL (one batch) to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	wantPaths := map[string]bool{ep1Path: true, ep2Path: true}
	if len(batch) != 2 {
		t.Fatalf("expected 2 Deleted entries in one batch, got %+v", batch)
	}
	for _, u := range batch {
		if u.UpdateType != "Deleted" || !wantPaths[u.Path] {
			t.Errorf("unexpected entry %+v", u)
		}
		delete(wantPaths, u.Path)
	}
	if len(wantPaths) != 0 {
		t.Errorf("missing entries for: %v", wantPaths)
	}
}

// TestApplyProposalHandler_MoviesDedupLoser_NotifiesJellyfin is row 7 end to
// end: the removed tracked loser's exact library FilePath reaches Jellyfin;
// the winner never moved, so it never appears.
func TestApplyProposalHandler_MoviesDedupLoser_NotifiesJellyfin(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeTestVideoFile(t, dir, "loser.mkv", 10)
	winnerPath := writeTestVideoFile(t, dir, "winner.mkv", 10)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	tracked, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: loserPath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
		{
			Status: proposals.Pending, Title: "X", TMDBID: 1, RootFolderPath: dir,
			Candidates: []proposals.Candidate{
				{Label: "tracked", Path: loserPath, TrackedID: int(tracked.ID)},
				{Label: "winner", Path: winnerPath, Winner: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 1 || batch[0].Path != loserPath || batch[0].UpdateType != "Deleted" {
		t.Errorf("expected a single Deleted PathChange for the exact removed loser path %q, got %+v", loserPath, batch)
	}
}

// TestApplyProposalHandler_SeriesDedupLoser_NotifiesJellyfin is row 8 end to
// end.
func TestApplyProposalHandler_SeriesDedupLoser_NotifiesJellyfin(t *testing.T) {
	dir := t.TempDir()
	loserPath := writeTestVideoFile(t, dir, "loser.mkv", 10)
	winnerPath := writeTestVideoFile(t, dir, "winner.mkv", 10)

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	series, err := libStore.UpsertSeries(ctx, library.Series{TMDBID: 1, Title: "X", RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tracked, err := libStore.UpsertEpisode(ctx, library.Episode{SeriesID: series.ID, SeasonNumber: 1, EpisodeNumber: 1, FilePath: loserPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Series, proposals.Dedup, []proposals.Proposal{
		{
			Status: proposals.Pending, Title: "X", TMDBID: 1, SeasonNumber: 1, EpisodeNumber: 1, RootFolderPath: dir,
			Candidates: []proposals.Candidate{
				{Label: "tracked", Path: loserPath, TrackedID: int(tracked.ID)},
				{Label: "winner", Path: winnerPath, Winner: true},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if jf.CallCount() != 1 {
		t.Fatalf("expected exactly one notify call to Jellyfin, got %d", jf.CallCount())
	}
	batch := jf.Batches()[0]
	if len(batch) != 1 || batch[0].Path != loserPath || batch[0].UpdateType != "Deleted" {
		t.Errorf("expected a single Deleted PathChange for the exact removed loser path %q, got %+v", loserPath, batch)
	}
}

// TestApplyProposalHandler_DedupKeepAll_NoJellyfinNotify is Edge #3:
// keepAll removes nothing, so it must produce zero notify calls.
func TestApplyProposalHandler_DedupKeepAll_NoJellyfinNotify(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	tracked, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: "/a.mkv", RootFolderPath: "/x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(0)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Dedup, []proposals.Proposal{
		{
			Status: proposals.Pending,
			Candidates: []proposals.Candidate{
				{Label: "a", Path: "/a.mkv", TrackedID: int(tracked.ID)},
				{Label: "b", Path: "/b.mkv"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(applyProposalRequest{KeepAll: true})
	applyProposal(t, srv, saved[0].ID, body)

	if jf.CallCount() != 0 {
		t.Errorf("expected zero notify calls for keepAll, got %d: %+v", jf.CallCount(), jf.Batches())
	}
}

// TestApplyProposalHandler_JellyfinBestEffort_ApplyStillSucceeds is
// Guardrail #1 / Acceptance #5: a downstream Jellyfin failure must never
// fail SAK's own Apply, which has already committed by the time notify
// runs.
func TestApplyProposalHandler_JellyfinBestEffort_ApplyStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	jf := newFakeJellyfin(http.StatusInternalServerError)
	if err := connStore.Upsert(ctx, "jellyfin", jf.Server(t).URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TrackedID: int(item.ID)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	// applyProposal itself asserts a 200 status — the point of this test.
	applied := applyProposal(t, srv, saved[0].ID, nil)
	if applied.Status != proposals.Applied {
		t.Errorf("expected the proposal to still be marked Applied despite the Jellyfin 500, got %+v", applied)
	}
	if jf.CallCount() != 1 {
		t.Errorf("expected the notify attempt to still have been made (and failed), got %d calls", jf.CallCount())
	}
}

// TestApplyProposalHandler_MoviesApply_StashConnectionConfigured_SendsNothingToStash
// proves the hardcoded per-mode scoping (CLAUDE.md Mission / mode.Build):
// even with a "stash" connection fully configured, a Movies Apply's
// sess.Stash is nil (Stash is Adult-only), so nothing is ever sent to it.
func TestApplyProposalHandler_MoviesApply_StashConnectionConfigured_SendsNothingToStash(t *testing.T) {
	stashSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to Stash — sess.Stash must be nil for Movies mode: %s %s", r.Method, r.URL.Path)
	}))
	defer stashSrv.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	item, err := libStore.Upsert(ctx, library.Item{Mode: mode.Movies, TMDBID: 1, Title: "X", FilePath: filePath, RootFolderPath: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "stash", stashSrv.URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	saved, err := propStore.ReplacePending(ctx, mode.Movies, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "X", TrackedID: int(item.ID)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	// Success here (no t.Fatalf inside the fake Stash handler having fired)
	// is the assertion: zero requests reached Stash for a Movies Apply.
	applyProposal(t, srv, saved[0].ID, nil)
}

// --- Slice 4: Adult rename/purge/dedup -> Stash ---------------------------

// fakeStash stands in for a live local Stash instance's GraphQL endpoint,
// recording every metadataScan/metadataClean mutation's decoded input (in
// call order) — the Adult counterpart to fakeJellyfin above, mirroring
// internal/mode's own stashRecorder test fake for the same reason: assert
// exactly what Session.NotifyPlayers put on the wire.
type fakeStash struct {
	mu         sync.Mutex
	scanCalls  []map[string]any
	cleanCalls []map[string]any
	status     int // non-zero overrides the normal 200 response, for best-effort tests
}

func newFakeStash(status int) *fakeStash {
	return &fakeStash{status: status}
}

func (f *fakeStash) Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				Input map[string]any `json:"input"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding fake Stash request body: %v", err)
		}
		f.mu.Lock()
		switch {
		case strings.Contains(req.Query, "metadataClean"):
			f.cleanCalls = append(f.cleanCalls, req.Variables.Input)
		case strings.Contains(req.Query, "metadataScan"):
			f.scanCalls = append(f.scanCalls, req.Variables.Input)
		default:
			f.mu.Unlock()
			t.Fatalf("unexpected stash mutation query: %s", req.Query)
			return
		}
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "metadataClean"):
			w.Write([]byte(`{"data":{"metadataClean":"clean-job"}}`))
		case strings.Contains(req.Query, "metadataScan"):
			w.Write([]byte(`{"data":{"metadataScan":"scan-job"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeStash) ScanCalls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.scanCalls))
	copy(out, f.scanCalls)
	return out
}

func (f *fakeStash) CleanCalls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.cleanCalls))
	copy(out, f.cleanCalls)
	return out
}

// fakeAdultServarr serves just enough of Whisparr V3's API for rename/purge/
// dedup Apply against sess.Servarr: registering a scene (POST /api/v3/movie),
// deleting a tracked one (DELETE /api/v3/movie/{id}), and the downloaded-
// files scan trigger (POST /api/v3/command). addStatus/scanStatus let a test
// force either call to fail, for the partial-success tests.
type fakeAdultServarr struct {
	mu           sync.Mutex
	addedID      int
	addStatus    int
	scanStatus   int
	addBodies    []map[string]any
	deletedIDs   []string
	scanTriggers int
}

func (f *fakeAdultServarr) Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/movie":
			f.mu.Lock()
			if f.addStatus != 0 {
				status := f.addStatus
				f.mu.Unlock()
				w.WriteHeader(status)
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.addBodies = append(f.addBodies, body)
			id := f.addedID
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": id})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v3/movie/"):
			f.mu.Lock()
			f.deletedIDs = append(f.deletedIDs, strings.TrimPrefix(r.URL.Path, "/api/v3/movie/"))
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			f.mu.Lock()
			if f.scanStatus != 0 {
				status := f.scanStatus
				f.mu.Unlock()
				w.WriteHeader(status)
				return
			}
			f.scanTriggers++
			f.mu.Unlock()
			w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected whisparr request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func adultProposalBase() proposals.Proposal {
	return proposals.Proposal{
		Status: proposals.Pending, Title: "Some Scene",
		ForeignID: "a29768db-b3cd-4a71-a75e-4294373207bb", ItemType: "scene",
		QualityProfileID: 4,
	}
}

// TestApplyProposalHandler_AdultRename_NotifiesStash proves row 3's dir-change
// path end to end: the moved file's ACTUAL unique destination reaches Stash
// as a phash-free scan (RescanPaths, scanGeneratePhashes=false), and the
// vacated SourcePath reaches it as a clean (CleanMetadata).
func TestApplyProposalHandler_AdultRename_NotifiesStash(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "Adult")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Some.Scene.mp4")
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{addedID: 77}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := adultProposalBase()
	p.SourceName, p.SourcePath, p.RootFolderPath = "Some Scene", sourcePath, destRoot
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applied := applyProposal(t, srv, saved[0].ID, nil)
	if applied.TrackedID != 77 {
		t.Fatalf("expected the registered id 77, got %d", applied.TrackedID)
	}

	wantDest := filepath.Join(destRoot, "Some.Scene.mp4")
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Errorf("expected the source file to be gone, stat returned: %v", err)
	}
	if _, err := os.Stat(wantDest); err != nil {
		t.Fatalf("expected the file to have moved to %q: %v", wantDest, err)
	}

	scanCalls, cleanCalls := stash.ScanCalls(), stash.CleanCalls()
	if len(scanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataScan call, got %d: %+v", len(scanCalls), scanCalls)
	}
	scanPaths, _ := scanCalls[0]["paths"].([]any)
	if len(scanPaths) != 1 || scanPaths[0] != wantDest {
		t.Errorf("expected scan of [%q], got %+v", wantDest, scanCalls[0]["paths"])
	}
	if scanCalls[0]["scanGeneratePhashes"] != false {
		t.Errorf("expected phash-free scan (proving RescanPaths not ScanPaths was used), got %v", scanCalls[0]["scanGeneratePhashes"])
	}
	if len(cleanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataClean call, got %d: %+v", len(cleanCalls), cleanCalls)
	}
	cleanPaths, _ := cleanCalls[0]["paths"].([]any)
	if len(cleanPaths) != 1 || cleanPaths[0] != sourcePath {
		t.Errorf("expected clean of [%q], got %+v", sourcePath, cleanCalls[0]["paths"])
	}
	if whisparr.scanTriggers != 1 {
		t.Errorf("expected the existing Whisparr downloaded-files scan trigger to still fire once, got %d", whisparr.scanTriggers)
	}
}

// TestApplyProposalHandler_AdultRenameNoMove_NoStashNotify is Edge #1: when
// SourcePath's directory already matches RootFolderPath, rename.Apply never
// attempts a relocate, so Stash must receive zero notify calls even though
// the proposal still registers successfully.
func TestApplyProposalHandler_AdultRenameNoMove_NoStashNotify(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{addedID: 1}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := adultProposalBase()
	p.SourceName = "Some Scene"
	p.SourcePath = "/media/Adult/Some.Scene.mp4"
	p.RootFolderPath = "/media/Adult"
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if got := len(stash.ScanCalls()) + len(stash.CleanCalls()); got != 0 {
		t.Errorf("expected zero Stash calls for a same-root (no-move) rename, got %d", got)
	}
}

// TestApplyProposalHandler_AdultRenamePartialSuccess_ScanTriggerFails is the
// single most important test in this slice, proving Critic fix #3: Relocate
// succeeds (the file physically moves) but the follow-up ScanForDownloaded
// call fails. Apply's pre-existing partial-success design still marks the
// proposal Applied (trackedID != 0, registered via Add). What's new here is
// that Stash is ALSO still notified of the move — a phantom scene would
// otherwise result, since the file really did move but Stash was never told.
func TestApplyProposalHandler_AdultRenamePartialSuccess_ScanTriggerFails(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "Adult")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Some.Scene.mp4")
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{addedID: 77, scanStatus: http.StatusInternalServerError}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := adultProposalBase()
	p.SourceName, p.SourcePath, p.RootFolderPath = "Some Scene", sourcePath, destRoot
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	// The scan-trigger failure surfaces as a non-200 from Apply (existing
	// behavior, unrelated to this slice) — but the proposal must still have
	// been marked Applied underneath, since Add itself succeeded.
	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a non-200 response surfacing the scan-trigger failure, got %d", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/adult/rename/proposals")
	if err != nil {
		t.Fatalf("list GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(getResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].Status != proposals.Applied || listed[0].TrackedID != 77 {
		t.Fatalf("expected the proposal to still be marked Applied with trackedID 77 despite the scan-trigger failure, got %+v", listed)
	}

	wantDest := filepath.Join(destRoot, "Some.Scene.mp4")
	if _, err := os.Stat(wantDest); err != nil {
		t.Fatalf("expected the file to have actually moved to %q despite the later failure: %v", wantDest, err)
	}

	scanCalls, cleanCalls := stash.ScanCalls(), stash.CleanCalls()
	if len(scanCalls) != 1 || scanCalls[0]["paths"].([]any)[0] != wantDest {
		t.Fatalf("expected Stash to still be notified of the move (scan %q) despite the scan-trigger failure, got %+v", wantDest, scanCalls)
	}
	if len(cleanCalls) != 1 || cleanCalls[0]["paths"].([]any)[0] != sourcePath {
		t.Fatalf("expected Stash to still be notified of the vacated source (clean %q) despite the scan-trigger failure, got %+v", sourcePath, cleanCalls)
	}
}

// TestApplyProposalHandler_AdultRenamePartialSuccess_AddFails covers the
// OTHER partial-success sub-case Critic fix #3 targets specifically: Relocate
// succeeds but Add itself fails, so trackedID never becomes nonzero and the
// proposal is NOT marked Applied (existing behavior, unchanged) — but the
// file has still physically moved, so Stash must be told regardless, or a
// phantom scene results with no corresponding SAK record at all.
func TestApplyProposalHandler_AdultRenamePartialSuccess_AddFails(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "incoming")
	destRoot := filepath.Join(base, "Adult")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sourcePath := filepath.Join(sourceRoot, "Some.Scene.mp4")
	if err := os.WriteFile(sourcePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{addStatus: http.StatusInternalServerError}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := adultProposalBase()
	p.SourceName, p.SourcePath, p.RootFolderPath = "Some Scene", sourcePath, destRoot
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Rename, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/proposals/"+strconv.FormatInt(saved[0].ID, 10)+"/apply", "application/json", nil)
	if err != nil {
		t.Fatalf("apply POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("expected a non-200 response surfacing the Add failure, got %d", resp.StatusCode)
	}

	getResp, err := http.Get(srv.URL + "/api/modes/adult/rename/proposals")
	if err != nil {
		t.Fatalf("list GET failed: %v", err)
	}
	defer getResp.Body.Close()
	var listed []proposals.Proposal
	json.NewDecoder(getResp.Body).Decode(&listed)
	if len(listed) != 1 || listed[0].Status != proposals.Pending {
		t.Fatalf("expected the proposal to remain Pending (Add never succeeded, so trackedID stayed 0), got %+v", listed)
	}

	wantDest := filepath.Join(destRoot, "Some.Scene.mp4")
	if _, err := os.Stat(wantDest); err != nil {
		t.Fatalf("expected the file to have actually moved to %q despite Add failing afterward: %v", wantDest, err)
	}

	scanCalls, cleanCalls := stash.ScanCalls(), stash.CleanCalls()
	if len(scanCalls) != 1 || scanCalls[0]["paths"].([]any)[0] != wantDest {
		t.Fatalf("expected Stash to still be notified of the move (scan %q) even though Add failed and nothing got MarkApplied, got %+v", wantDest, scanCalls)
	}
	if len(cleanCalls) != 1 || cleanCalls[0]["paths"].([]any)[0] != sourcePath {
		t.Fatalf("expected Stash to still be notified of the vacated source (clean %q) even though Add failed, got %+v", sourcePath, cleanCalls)
	}
}

// TestApplyProposalHandler_AdultPurge_NotifiesStash is row 6 end to end:
// p.SourcePath reaches Stash as a clean, with no corresponding scan.
func TestApplyProposalHandler_AdultPurge_NotifiesStash(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sourcePath := "/media/Adult/Flagged Scene"
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "Flagged Scene", SourcePath: sourcePath, TrackedID: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if len(whisparr.deletedIDs) != 1 || whisparr.deletedIDs[0] != "2" {
		t.Fatalf("expected DeleteTracked(2) against Whisparr, got %+v", whisparr.deletedIDs)
	}
	if got := len(stash.ScanCalls()); got != 0 {
		t.Errorf("expected zero metadataScan calls for a purge, got %d", got)
	}
	cleanCalls := stash.CleanCalls()
	if len(cleanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataClean call, got %d: %+v", len(cleanCalls), cleanCalls)
	}
	cleanPaths, _ := cleanCalls[0]["paths"].([]any)
	if len(cleanPaths) != 1 || cleanPaths[0] != sourcePath {
		t.Errorf("expected clean of [%q], got %+v", sourcePath, cleanCalls[0]["paths"])
	}
}

// TestApplyProposalHandler_AdultDedupLoser_NotifiesStash is row 9 end to end:
// the removed tracked loser's candidate path reaches Stash as a clean; the
// newly-registered winner never moved, so it never appears.
func TestApplyProposalHandler_AdultDedupLoser_NotifiesStash(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{addedID: 88}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loserPath := "/tracked/scene.mp4"
	p := adultProposalBase()
	p.Candidates = []proposals.Candidate{
		{Label: "tracked", Path: loserPath, TrackedID: 9},
		{Label: "winner", Path: "/media/Adult/winner.mp4", Winner: true},
	}
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	applyProposal(t, srv, saved[0].ID, nil)

	if len(whisparr.deletedIDs) != 1 || whisparr.deletedIDs[0] != "9" {
		t.Fatalf("expected DeleteTracked(9) against Whisparr for the tracked loser, got %+v", whisparr.deletedIDs)
	}
	if got := len(stash.ScanCalls()); got != 0 {
		t.Errorf("expected zero metadataScan calls — the winner never moved, so nothing is Created — got %d", got)
	}
	cleanCalls := stash.CleanCalls()
	if len(cleanCalls) != 1 {
		t.Fatalf("expected exactly 1 metadataClean call, got %d: %+v", len(cleanCalls), cleanCalls)
	}
	cleanPaths, _ := cleanCalls[0]["paths"].([]any)
	if len(cleanPaths) != 1 || cleanPaths[0] != loserPath {
		t.Errorf("expected clean of [%q], got %+v", loserPath, cleanCalls[0]["paths"])
	}
	if whisparr.scanTriggers != 1 {
		t.Errorf("expected the existing Whisparr downloaded-files scan trigger to still fire once for the newly-registered winner, got %d", whisparr.scanTriggers)
	}
}

// TestApplyProposalHandler_AdultDedupKeepAll_NoStashNotify is Edge #3's Adult
// counterpart: keepAll removes nothing, so Stash must receive zero calls.
func TestApplyProposalHandler_AdultDedupKeepAll_NoStashNotify(t *testing.T) {
	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stash := newFakeStash(0)
	if err := connStore.Upsert(ctx, "stash", stash.Server(t).URL, "stash-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := adultProposalBase()
	p.Candidates = []proposals.Candidate{
		{Label: "tracked", Path: "/tracked/scene.mp4", TrackedID: 9},
		{Label: "winner", Path: "/media/Adult/winner.mp4"},
	}
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Dedup, []proposals.Proposal{p})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	body, _ := json.Marshal(applyProposalRequest{KeepAll: true})
	applyProposal(t, srv, saved[0].ID, body)

	if got := len(stash.ScanCalls()) + len(stash.CleanCalls()); got != 0 {
		t.Errorf("expected zero Stash calls for keepAll, got %d", got)
	}
}

// TestApplyProposalHandler_AdultApply_JellyfinConnectionConfigured_SendsNothingToJellyfin
// proves the hardcoded scoping in the other direction from the existing
// Movies test above: even with a "jellyfin" connection fully configured, an
// Adult Apply's sess.Jellyfin is nil (Jellyfin is Movies/Series-only), so
// nothing is ever sent to it.
func TestApplyProposalHandler_AdultApply_JellyfinConnectionConfigured_SendsNothingToJellyfin(t *testing.T) {
	jfSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to Jellyfin — sess.Jellyfin must be nil for Adult mode: %s %s", r.Method, r.URL.Path)
	}))
	defer jfSrv.Close()

	connStore, propStore, allowStore, settingsStore, grabsStore, libStore := testStores(t)
	ctx := context.Background()
	whisparr := &fakeAdultServarr{}
	if err := connStore.Upsert(ctx, "whisparr", whisparr.Server(t).URL, "test-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := connStore.Upsert(ctx, "jellyfin", jfSrv.URL, "jf-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sourcePath := "/media/Adult/Flagged Scene"
	saved, err := propStore.ReplacePending(ctx, mode.Adult, proposals.Purge, []proposals.Proposal{
		{Status: proposals.Pending, Title: "Flagged Scene", SourcePath: sourcePath, TrackedID: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	srv := httptest.NewServer(NewMux(testHTTPClient(), connStore, propStore, allowStore, testProber(t), testPHasher(t), testVideoHasher(t), settingsStore, grabsStore, libStore))
	defer srv.Close()

	// Success here (no t.Fatalf inside the fake Jellyfin handler having
	// fired) is the assertion: zero requests reached Jellyfin for an Adult
	// Apply.
	applyProposal(t, srv, saved[0].ID, nil)
}
