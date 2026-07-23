package library

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

// VMAFScore caches a computed VMAF perceptual-quality score for one Dedup
// candidate file measured against a reference (the group's primary at compute
// time). Unlike orphan_phashes' single-path identity, a score is inherently a
// pair — the same candidate scores differently against a different primary —
// so the cache is keyed on (candidate_path, reference_path).
//
// A cache entry is valid only while the candidate file's size+mtime still
// match the stored identity fields (mirrors OrphanPHash's invalidation): a
// replaced or re-encoded file at the same path is detected and the score
// recomputed. Cleanup is event-driven, not a scan-time sweep — the row is
// pruned the moment either side's file is deleted (PruneVMAFScoresForPath),
// a stronger invalidation model than orphan_phashes' periodic
// DeleteOrphanPHashesNotIn.
type VMAFScore struct {
	CandidatePath      string
	CandidateFileSize  int64
	CandidateFileMTime string
	ReferencePath      string
	Score              float64
	ComputedAt         string
}

// VMAFFileIdentity returns the size and UTC RFC3339Nano mtime used as the
// candidate side of the vmaf_scores cache key — identical logic to
// orphanFileIdentity. Exported (unlike orphanFileIdentity) because
// internal/api's on-demand VMAF handler must stamp the exact same identity
// format when it upserts a freshly-computed score; sharing this function
// instead of a second copy is what keeps the two format strings from ever
// silently drifting apart.
func VMAFFileIdentity(path string) (size int64, mtime string, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, "", err
	}
	return fi.Size(), fi.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// GetVMAFScore returns the cached VMAFScore for the (candidatePath,
// referencePath) pair, or (zero, nil) when no row exists. It is a raw DB
// lookup and deliberately does NOT stat the file — the size+mtime identity
// check lives in GetValidVMAFScore, exactly as GetOrphanPHash stays raw while
// LoadOrComputeOrphanPHash owns the identity comparison. Keeping Get raw is
// what lets a caller assert a row is truly gone (not merely stale because its
// file happens to be missing). It returns an error only for unexpected
// database failures.
func (s *Store) GetVMAFScore(ctx context.Context, candidatePath, referencePath string) (VMAFScore, error) {
	var v VMAFScore
	err := s.db.QueryRowContext(ctx,
		`SELECT candidate_path, candidate_file_size, candidate_file_mtime, reference_path, score, computed_at
		   FROM vmaf_scores WHERE candidate_path = ? AND reference_path = ?`,
		candidatePath, referencePath).
		Scan(&v.CandidatePath, &v.CandidateFileSize, &v.CandidateFileMTime, &v.ReferencePath, &v.Score, &v.ComputedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return VMAFScore{}, nil
	}
	return v, err
}

// GetValidVMAFScore returns the cached score for (candidatePath,
// referencePath) only when a row exists AND the candidate file's current
// size+mtime still match the stored identity — the same invalidation check
// orphan_phashes applies in LoadOrComputeOrphanPHash. It returns
// (score, true, nil) on a valid cache hit and (0, false, nil) on a miss or a
// stale entry (candidate file changed, missing, or unreadable). The Stage 2
// on-demand compute path calls this and recomputes+upserts on a false result.
func (s *Store) GetValidVMAFScore(ctx context.Context, candidatePath, referencePath string) (float64, bool, error) {
	cached, err := s.GetVMAFScore(ctx, candidatePath, referencePath)
	if err != nil {
		return 0, false, err
	}
	if cached.CandidatePath == "" {
		return 0, false, nil // no cached row for this pair
	}
	size, mtime, err := VMAFFileIdentity(candidatePath)
	if err != nil {
		return 0, false, nil // file gone/unreadable — treat as invalid
	}
	if cached.CandidateFileSize != size || cached.CandidateFileMTime != mtime {
		return 0, false, nil // file changed since the score was computed — stale
	}
	return cached.Score, true, nil
}

// UpsertVMAFScore inserts or replaces the cached score for a (candidate,
// reference) pair, stamping the candidate file's size+mtime as the
// invalidation identity. Called after a fresh VMAF computation to amortise
// ffmpeg cost across subsequent views of the same unchanged group.
func (s *Store) UpsertVMAFScore(ctx context.Context, v VMAFScore) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vmaf_scores (candidate_path, candidate_file_size, candidate_file_mtime, reference_path, score, computed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(candidate_path, reference_path) DO UPDATE SET
		   candidate_file_size  = excluded.candidate_file_size,
		   candidate_file_mtime = excluded.candidate_file_mtime,
		   score                = excluded.score,
		   computed_at          = excluded.computed_at`,
		v.CandidatePath, v.CandidateFileSize, v.CandidateFileMTime, v.ReferencePath, v.Score, v.ComputedAt)
	return err
}

// DeleteVMAFScoresForPath removes every cached score where path sits on either
// side of the pair — a deleted file might be the candidate OR the reference
// (the primary) of a cached comparison, and both become meaningless once the
// file is gone. This is the raw, error-returning deletion; PruneVMAFScoresForPath
// wraps it best-effort for the Apply-delete call sites.
func (s *Store) DeleteVMAFScoresForPath(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM vmaf_scores WHERE candidate_path = ? OR reference_path = ?`, path, path)
	return err
}

// PruneVMAFScoresForPath is the best-effort cache-cleanup helper Dedup's
// Apply-delete sites call immediately after a losing candidate's file is
// deleted. It swallows any error: file deletion isn't transactional, so this
// is deliberately NOT run in a shared transaction with os.Remove — a crash
// (or a DB error) between the file delete and this call only leaves a
// harmless, self-correcting stale row, which the next compute attempt against
// that path re-derives identity for and overwrites, or which its own file's
// eventual re-deletion clears.
func (s *Store) PruneVMAFScoresForPath(ctx context.Context, path string) {
	if path == "" {
		return
	}
	_ = s.DeleteVMAFScoresForPath(ctx, path)
}
