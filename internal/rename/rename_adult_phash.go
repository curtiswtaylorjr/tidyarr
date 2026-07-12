package rename

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/curtiswtaylorjr/sakms/internal/identify"
	"github.com/curtiswtaylorjr/sakms/internal/mediainfo"
	"github.com/curtiswtaylorjr/sakms/internal/mode"
	"github.com/curtiswtaylorjr/sakms/internal/proposals"
	"github.com/curtiswtaylorjr/sakms/internal/servarr"
)

// adultHashWorkers bounds how many files scanAdultPhashFirst hashes+probes
// concurrently. Each Hash shells out ~25 ffmpeg frame extractions, so an
// unbounded fan-out on a large Adult library would thrash the host; a fixed
// small pool caps concurrent ffmpeg processes at 4 while still finishing far
// faster than a strictly sequential decode. Per-file wall time is already
// bounded by videophash's own internal ~2min timeout, so no Scan-wide deadline
// is imposed here (a global cutoff would wrongly dump still-unhashed files to
// the slower legacy path on a legitimately large library — see the impl plan).
const adultHashWorkers = 4

// PHasher computes a file's StashDB-compatible perceptual hash. A rename-local
// structural interface (satisfied by *videophash.Hasher) so this package never
// imports internal/videophash — the same seam pattern internal/dedup uses for
// its own injected hasher.
type PHasher interface {
	Hash(ctx context.Context, path string) (string, error)
}

// Prober reads a file's duration (among other fields) directly off disk.
// Structural, satisfied by *mediainfo.Prober, so give-back's DurationSeconds is
// sourced locally rather than from a live Stash read.
type Prober interface {
	Probe(ctx context.Context, path string) (*mediainfo.Probe, error)
}

// adultCandidate pairs one unmapped folder with the root it was found under
// — the unit scanAdultPhashFirst batches through the phash-first pipeline.
type adultCandidate struct {
	root servarr.RootFolder
	uf   servarr.UnmappedFolder
}

// hashResult holds one candidate's locally-computed identification inputs.
// ok is false when the file couldn't be hashed at all — that candidate then
// degrades to the legacy AI/text pipeline on its own, never failing the batch.
type hashResult struct {
	phash    string
	duration int
	ok       bool
}

// scanAdultPhashFirst resolves candidates via SAK's OWN StashDB-compatible
// perceptual hash first — computed locally per file via the injected hasher
// (internal/videophash) rather than read from a live Stash — then a batched
// StashDB->FansDB->TPDB cascade lookup (identify.GiveBack's configured boxes),
// falling back to the legacy AI/text identification pipeline (proposeOneAdult)
// for anything the cascade can't resolve.
//
// This restores phash as Adult's PRIMARY identification signal (matching the
// prior CLI this was ported from), tried before AI/web-search rather than as a
// supplementary check — see docs/ROADMAP.md's phash decision entry. It no
// longer needs a live Stash instance: the hash is computed synchronously, so
// the old force-generate/poll rescan machinery is gone. sess.Identify is
// already guaranteed non-nil for Adult by Scan's own upfront check.
//
// DurationSeconds (required by fingerprint give-back, which silently no-ops on
// a non-positive duration) is sourced from the injected prober — NOT from the
// hasher, which returns only a hash string. A file that hashes but fails to
// probe simply carries duration 0, so give-back fails open for that ONE file.
// A file that fails to hash degrades to the legacy pipeline for that ONE file
// (per-file fail-open, replacing the old all-or-nothing Stash fail-open).
//
// The build phase is a single order-preserving loop over every candidate:
// each hashed candidate (r.ok) has its local phash/duration stamped onto its
// proposal regardless of HOW that proposal was resolved — a fingerprint
// cascade hit, or the legacy AI/text fallback (proposeOneAdult) for a cascade
// miss. This matters because give-back at Apply only fires when PHash is set;
// previously only cascade hits carried a phash, so a candidate that hashed
// fine but text-matched instead reached Apply with GiveBackBox set and
// PHash == "", silently losing give-back. A cascade lookup error is handled
// the same way (fail open into the unified loop) so those candidates also
// keep their local phash. Output order is candidate-index order (interleaved
// cascade hits and fallbacks), not "cascade hits first" as before.
func scanAdultPhashFirst(
	ctx context.Context, sess *mode.Session, hasher PHasher, prober Prober,
	candidates []adultCandidate, tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) []proposals.Proposal {
	files := make([]adultFileID, len(candidates))
	for i, c := range candidates {
		// stem/parentName exactly reproduce proposeOneAdult's own
		// ident.Identify(ctx, uf.Name, filepath.Base(root.Path)) call, so the
		// cascade-miss fallback behaves identically to before this extraction.
		files[i] = adultFileID{path: c.uf.Path, stem: c.uf.Name, parentName: filepath.Base(c.root.Path)}
	}
	ids := identifyAdultFiles(ctx, sess, hasher, prober, files)

	// Single order-preserving loop over candidates; stamp phash/duration on
	// EVERY hashed candidate — cascade hit or legacy/text fallback alike.
	out := make([]proposals.Proposal, 0, len(candidates))
	for i, c := range candidates {
		id := ids[i]
		p := buildAdultProposal(sess.Mode, c.root, c.uf, id.match, id.err, tracked, profiles)
		if id.hashed {
			p.PHash = id.phash
			p.DurationSeconds = id.duration
		}
		out = append(out, p)
	}
	return out
}

// adultFileID names one file to run through the phash-first identification
// cascade: path is hashed+probed locally, and (stem, parentName) feed the
// legacy AI/text Identify fallback used for a fingerprint-cascade miss.
type adultFileID struct {
	path       string
	stem       string
	parentName string
}

// adultIdentification is the resolved identity for one adultFileID: the
// MatchResult (nil if nothing resolved it), any error from the legacy Identify
// fallback, and the locally-computed phash/duration. hashed is false when the
// file couldn't be hashed at all — that file degraded straight to the legacy
// pipeline and carries no phash (so give-back and the filename tag are skipped
// for it downstream). Both the Servarr-backed scanAdultPhashFirst and the
// library-backed ScanLibraryAdult build proposals from this one shape, so the
// phash-first-then-Identify cascade lives in exactly one place.
type adultIdentification struct {
	match    *identify.MatchResult
	err      error
	phash    string
	duration int
	hashed   bool
}

// identifyAdultFiles runs the phash-first cascade over files: a bounded
// concurrent local hash+probe phase, one batched StashDB->FansDB->TPDB
// fingerprint lookup, then the legacy AI/text Identify fallback for anything
// the cascade couldn't resolve. Extracted from scanAdultPhashFirst (rather
// than duplicated) so the library-backed path calls the exact same cascade —
// see rename_adult_library.go. Output is candidate-index order.
//
// Concurrency/fail-open semantics are unchanged from the original inline
// implementation: each goroutine writes only its own results[i] (no shared
// map, no mutex), a file that fails to hash falls open to the legacy pipeline
// for THAT file only, a file that hashes but fails to probe carries duration 0
// (give-back fails open for it), and a LookupFingerprints error fails the
// whole batch open into the legacy fallback while every file still keeps its
// local phash. sess.Identify is guaranteed non-nil by every caller.
func identifyAdultFiles(ctx context.Context, sess *mode.Session, hasher PHasher, prober Prober, files []adultFileID) []adultIdentification {
	results := make([]hashResult, len(files))
	sem := make(chan struct{}, adultHashWorkers)
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			h, err := hasher.Hash(ctx, path)
			if err != nil {
				return // ok stays false -> this file routes to legacy
			}
			r := hashResult{phash: h, ok: true}
			if pr, perr := prober.Probe(ctx, path); perr == nil {
				// float64 seconds -> int, matching the old int(f.Duration).
				r.duration = int(pr.Duration)
			}
			results[i] = r
		}(i, files[i].path)
	}
	wg.Wait()

	var phashes []string
	for i := range files {
		if results[i].ok {
			phashes = append(phashes, results[i].phash)
		}
	}

	matches, err := sess.Identify.LookupFingerprints(ctx, phashes)
	if err != nil {
		matches = nil // fail open: everything falls back, but still carries its local phash
	}

	out := make([]adultIdentification, len(files))
	for i, f := range files {
		r := results[i]
		id := adultIdentification{phash: r.phash, duration: r.duration, hashed: r.ok}
		if match, hit := matches[r.phash]; r.ok && hit {
			id.match = match
		} else {
			id.match, id.err = sess.Identify.Identify(ctx, f.stem, f.parentName)
		}
		out[i] = id
	}
	return out
}
