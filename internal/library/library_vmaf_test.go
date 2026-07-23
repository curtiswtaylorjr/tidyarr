package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVMAFScore_UpsertThenGet_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := VMAFScore{
		CandidatePath: "/movies/cand.mkv", CandidateFileSize: 1234, CandidateFileMTime: "2026-01-02T03:04:05Z",
		ReferencePath: "/movies/primary.mkv", Score: 96.5, ComputedAt: "2026-07-23T00:00:00Z",
	}
	if err := s.UpsertVMAFScore(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetVMAFScore(ctx, want.CandidatePath, want.ReferencePath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestGetVMAFScore_MissingReturnsZeroNoError(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetVMAFScore(context.Background(), "/nope.mkv", "/nada.mkv")
	if err != nil {
		t.Fatalf("expected no error for a missing row, got %v", err)
	}
	if got.CandidatePath != "" || got.Score != 0 {
		t.Errorf("expected the zero VMAFScore for a missing row, got %+v", got)
	}
}

// TestUpsertVMAFScore_ReplacesOnConflict proves the (candidate_path,
// reference_path) primary key drives an in-place update (score recompute
// against the same primary), not a duplicate row.
func TestUpsertVMAFScore_ReplacesOnConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := VMAFScore{
		CandidatePath: "/cand.mkv", CandidateFileSize: 10, CandidateFileMTime: "t1",
		ReferencePath: "/ref.mkv", Score: 50, ComputedAt: "c1",
	}
	if err := s.UpsertVMAFScore(ctx, base); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	updated := base
	updated.CandidateFileSize, updated.CandidateFileMTime, updated.Score, updated.ComputedAt = 20, "t2", 88.25, "c2"
	if err := s.UpsertVMAFScore(ctx, updated); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := s.GetVMAFScore(ctx, base.CandidatePath, base.ReferencePath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != updated {
		t.Errorf("expected the row overwritten in place, got %+v want %+v", got, updated)
	}
}

// TestGetValidVMAFScore_HonoursSizeMtimeIdentity is the cache-invalidation
// test: a cached score is served only while the candidate file's size+mtime
// still match the stored identity, exactly like OrphanPHash's invalidation.
func TestGetValidVMAFScore_HonoursSizeMtimeIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dir := t.TempDir()
	candPath := filepath.Join(dir, "cand.mkv")
	if err := os.WriteFile(candPath, make([]byte, 100), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	size, mtime, err := VMAFFileIdentity(candPath)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	refPath := "/primary.mkv"
	if err := s.UpsertVMAFScore(ctx, VMAFScore{
		CandidatePath: candPath, CandidateFileSize: size, CandidateFileMTime: mtime,
		ReferencePath: refPath, Score: 77.5, ComputedAt: "now",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Matching identity -> valid cache hit.
	score, ok, err := s.GetValidVMAFScore(ctx, candPath, refPath)
	if err != nil {
		t.Fatalf("get valid: %v", err)
	}
	if !ok || score != 77.5 {
		t.Fatalf("expected a valid cache hit of 77.5, got score=%v ok=%v", score, ok)
	}

	// Rewrite the file with a different size and a bumped mtime -> stale.
	if err := os.WriteFile(candPath, make([]byte, 200), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(candPath, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok, err := s.GetValidVMAFScore(ctx, candPath, refPath); err != nil || ok {
		t.Errorf("expected a stale (invalid) result after the file changed, got ok=%v err=%v", ok, err)
	}

	// Missing file -> invalid, not an error.
	if err := os.Remove(candPath); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, err := s.GetValidVMAFScore(ctx, candPath, refPath); err != nil || ok {
		t.Errorf("expected an invalid result for a missing file, got ok=%v err=%v", ok, err)
	}

	// No row at all -> miss, not an error.
	if _, ok, err := s.GetValidVMAFScore(ctx, "/never-cached.mkv", refPath); err != nil || ok {
		t.Errorf("expected a miss for an uncached pair, got ok=%v err=%v", ok, err)
	}
}

// TestPruneVMAFScoresForPath_DeletesEitherSideOfPair proves cleanup fires
// whether the deleted path was the candidate OR the reference of a cached
// pair, and leaves unrelated rows intact.
func TestPruneVMAFScoresForPath_DeletesEitherSideOfPair(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const victim = "/lib/victim.mkv"
	rows := []VMAFScore{
		{CandidatePath: victim, CandidateFileSize: 1, CandidateFileMTime: "a", ReferencePath: "/lib/primary.mkv", Score: 10, ComputedAt: "c"},
		{CandidatePath: "/lib/other.mkv", CandidateFileSize: 2, CandidateFileMTime: "b", ReferencePath: victim, Score: 20, ComputedAt: "c"},
		{CandidatePath: "/lib/unrelated.mkv", CandidateFileSize: 3, CandidateFileMTime: "d", ReferencePath: "/lib/primary.mkv", Score: 30, ComputedAt: "c"},
	}
	for _, r := range rows {
		if err := s.UpsertVMAFScore(ctx, r); err != nil {
			t.Fatalf("seed upsert: %v", err)
		}
	}

	s.PruneVMAFScoresForPath(ctx, victim)

	// The pair where victim was the candidate is gone.
	if got, err := s.GetVMAFScore(ctx, victim, "/lib/primary.mkv"); err != nil || got.CandidatePath != "" {
		t.Errorf("expected the candidate-side pair pruned, got %+v err=%v", got, err)
	}
	// The pair where victim was the reference is gone.
	if got, err := s.GetVMAFScore(ctx, "/lib/other.mkv", victim); err != nil || got.CandidatePath != "" {
		t.Errorf("expected the reference-side pair pruned, got %+v err=%v", got, err)
	}
	// The unrelated pair survives.
	if got, err := s.GetVMAFScore(ctx, "/lib/unrelated.mkv", "/lib/primary.mkv"); err != nil || got.Score != 30 {
		t.Errorf("expected the unrelated pair to survive, got %+v err=%v", got, err)
	}
}

// TestPruneVMAFScoresForPath_EmptyPathIsNoop guards the ""-path early return —
// a caller must never accidentally wipe rows keyed on an empty string.
func TestPruneVMAFScoresForPath_EmptyPathIsNoop(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertVMAFScore(ctx, VMAFScore{
		CandidatePath: "/keep.mkv", ReferencePath: "/primary.mkv", Score: 42, ComputedAt: "c",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	s.PruneVMAFScoresForPath(ctx, "")
	if got, err := s.GetVMAFScore(ctx, "/keep.mkv", "/primary.mkv"); err != nil || got.Score != 42 {
		t.Errorf("expected the row untouched by an empty-path prune, got %+v err=%v", got, err)
	}
}
