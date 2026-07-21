package phash

import (
	"fmt"
	"image"

	"github.com/ajdnik/imghash/v2"
	"github.com/ajdnik/imghash/v2/hashtype"
)

// Scheme tags every hash this package produces with the algorithm and frame
// count that produced it. A value cached under a different algorithm or
// sampling count is therefore self-identifying and treated as incomparable by
// SimilarityWithin, never silently mis-distanced. It is embedded in the stored
// hash string (see internal/db migration 0017 + internal/library threading),
// so a scheme change makes every cached value self-invalidating. Exported so
// the dedup cache layer can cheaply reject a stale-scheme cached hash by prefix
// before trusting a size+mtime identity match.
//
// This file is the SINGLE algorithm swap point. It ships imghash/v2's PDQ
// (256 bits/frame); the algorithm lives entirely in newAlgo, hashFrame, and
// this Scheme constant. The 256-bit width is NOT downstream-invisible: it
// drives the width-derived fixes in distance.go (byte-length-authoritative bit
// count) and internal/api/library.go (the threshold bound + stored-threshold
// scale tag, both keyed on PerFrameBits below). Composites are still compared
// as scheme-tagged byte blobs by Hamming distance regardless of which
// algorithm produced them; the scheme tag guarantees a value cached under a
// different algorithm or width is never silently mis-distanced.
//
// The tag is "pdq256/5f": swapping PHash->PDQ changed both the algorithm and
// the per-frame hash width (64 -> 256 bits), so under this package's
// no-silent-mis-compare rule the tag MUST change — every old "phash64*" cached
// value self-invalidates and recomputes on the next scan. Because the scale
// (PerFrameBits) also changed, per-mode stored thresholds tuned on the old
// 64-bit scale are version-gated to their PDQ default (see library.go's
// resolvePHashThreshold), not silently reinterpreted on the wider scale.
const Scheme = "pdq256/5f"

// Frames is the fixed number of evenly-spaced interior frames sampled per
// video to form one composite hash. Exported so the dedup layer can express
// its similarity threshold as a per-frame average, independent of this count.
const Frames = 5

// PerFrameBits is the width in bits of each per-frame perceptual hash the
// active algorithm (PDQ) produces (256 bits / 32 bytes). It is the SINGLE
// source of truth for both the API threshold bound and the stored-threshold
// scale tag in internal/api/library.go — no separate hardcoded width literal
// exists downstream, so the tunable range and the scale version tag track the
// active algorithm automatically. A future algorithm/width change updates this
// one constant and both follow.
const PerFrameBits = 256

// newAlgo constructs the perceptual-hash algorithm. Called from Hash (not
// New) deliberately: v2's constructor returns an error (NewPDQ() (PDQ,
// error)), and Hash already returns an error, so the swap stays isolated to
// this file instead of rippling into New's signature. Constructed with zero
// options, which yields imghash/v2's default PDQ (256-bit hash).
func newAlgo() (imghash.PDQ, error) {
	return imghash.NewPDQ()
}

// hashFrame returns the per-frame perceptual hash bytes for one decoded image.
// v2's PDQ.Calculate returns a hashtype.Hash interface plus an error; for PDQ
// the concrete value is a hashtype.Binary (type Binary []byte), so the result
// is type-asserted to it before extracting the raw bytes.
func hashFrame(a imghash.PDQ, img image.Image) ([]byte, error) {
	h, err := a.Calculate(img)
	if err != nil {
		return nil, err
	}
	b, ok := h.(hashtype.Binary)
	if !ok {
		return nil, fmt.Errorf("phash: expected hashtype.Binary, got %T", h)
	}
	return []byte(b), nil
}

// hammingBits is a plain popcount over the XOR of two equal-length byte
// slices — deliberately NOT imghash's similarity.Hamming, whose return
// semantics (raw bit count vs. a normalized fraction) could not be confirmed
// from its docs. A popcount is algorithm-agnostic and correct regardless of
// what any third-party helper returns, so distance.go never depends on
// unverified upstream semantics. Callers guarantee len(a) == len(b).
func hammingBits(a, b []byte) int {
	d := 0
	for i := range a {
		x := a[i] ^ b[i]
		for x != 0 {
			d += int(x & 1)
			x >>= 1
		}
	}
	return d
}
