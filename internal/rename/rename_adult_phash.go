package rename

import (
	"context"
	"sync"

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
func scanAdultPhashFirst(
	ctx context.Context, sess *mode.Session, hasher PHasher, prober Prober,
	candidates []adultCandidate, tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) []proposals.Proposal {
	// Bounded concurrent hash+probe phase. Each goroutine writes only to its
	// own results[i] index (no shared map, no mutex) so ordering is
	// deterministic and the phase is race-free.
	results := make([]hashResult, len(candidates))
	sem := make(chan struct{}, adultHashWorkers)
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, path string) {
			defer wg.Done()
			defer func() { <-sem }()
			h, err := hasher.Hash(ctx, path)
			if err != nil {
				return // ok stays false -> this candidate routes to legacy
			}
			r := hashResult{phash: h, ok: true}
			if pr, perr := prober.Probe(ctx, path); perr == nil {
				// float64 seconds -> int, matching the old int(f.Duration).
				r.duration = int(pr.Duration)
			}
			results[i] = r
		}(i, candidates[i].uf.Path)
	}
	wg.Wait()

	var phashes []string
	for i := range candidates {
		if results[i].ok {
			phashes = append(phashes, results[i].phash)
		}
	}

	matches, err := sess.Identify.LookupFingerprints(ctx, phashes)
	if err != nil {
		// Fail open: a cascade error shouldn't block the whole Adult scan —
		// every candidate still gets identified, just via the slower legacy path.
		return legacyProposeAll(ctx, sess, candidates, tracked, profiles)
	}

	// Sequential, order-preserving build phase: deterministic output.
	var out []proposals.Proposal
	var fallback []adultCandidate
	for i, c := range candidates {
		r := results[i]
		match, hit := matches[r.phash]
		if !r.ok || !hit {
			fallback = append(fallback, c)
			continue
		}
		p := buildAdultProposal(sess.Mode, c.root, c.uf, match, nil, tracked, profiles)
		p.PHash = r.phash
		p.DurationSeconds = r.duration
		out = append(out, p)
	}

	return append(out, legacyProposeAll(ctx, sess, fallback, tracked, profiles)...)
}

// legacyProposeAll runs the AI/text identification pipeline (proposeOneAdult)
// for every candidate the phash cascade didn't resolve.
func legacyProposeAll(
	ctx context.Context, sess *mode.Session,
	candidates []adultCandidate, tracked []servarr.TrackedItem, profiles []servarr.QualityProfile,
) []proposals.Proposal {
	out := make([]proposals.Proposal, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, proposeOneAdult(ctx, sess.Identify, sess.Mode, c.root, c.uf, tracked, profiles))
	}
	return out
}
