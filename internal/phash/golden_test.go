package phash

import (
	"encoding/hex"
	"testing"
)

// TestGolden_PDQOutputStable pins the exact per-frame PDQ bytes this package
// produces for a fixed set of deterministic synthetic inputs (broadbandImage
// seeds 0..7 at size 128 — the same generator calibrate_test uses). It is the
// self-enforcing guard for this package's #1 rule: a hash whose bytes change
// meaning must never be compared as if equivalent, so ANY change to the active
// algorithm's byte output (a library bump, a parameter change, a decode-path
// change) must fail here and force a deliberate Scheme bump.
//
// The pinned values below are imghash v2.5.2's PDQ output (256 bits / 32 bytes
// / 64 hex chars per frame), captured after the PHash->PDQ swap. Any diff here
// is either an unintended regression to investigate or an intended algorithm
// change that must be paired with a Scheme bump (so stale cached values
// self-invalidate) and a re-pin of these goldens.
func TestGolden_PDQOutputStable(t *testing.T) {
	// imghash v2.5.2 PDQ of broadbandImage(seed, 128), seeds 0..7.
	want := []string{
		"bf02b1d391f8f12eb1ac9b8e9f0791fcd807cf026259ee0162d14e4a42f76ec8",
		"b702c9bbc1fbf1b99a51c9f86a1def04e045bf04ae55fe0054aa3e0734163e07",
		"bf006f0ffb01ef0da707e153b2eff903ad0180fc5856f6a100fc14fb00fc14fa",
		"ff00b1a27f0c1f06c9f37f0128f1d15a7a59bf0806eec05f844f80fb00fe80ff",
		"ff02af0bf70a670b635bbf0f90a405f290ebd8f5e05dfa000e54c0f44cf4c4f4",
		"ff00ff01edaca95cfd0d7f000f6de5ad10fde70710f1c8b56e5230f262560072",
		"ff0053fa5b5103f8eb5ad30e5a078f0840ff644130fefe07b4e5ac07a4e58c87",
		"ff00ff0b7fa999a87d039da9f90295a9c0b47f007a5438fb84fd4616007f4256",
	}
	algo, err := newAlgo()
	if err != nil {
		t.Fatalf("constructing algo: %v", err)
	}
	for seed, wantHex := range want {
		h, err := hashFrame(algo, broadbandImage(seed, 128))
		if err != nil {
			t.Fatalf("seed %d: hashing: %v", seed, err)
		}
		if got := hex.EncodeToString(h); got != wantHex {
			t.Errorf("seed %d: PDQ output changed: got %s, want %s — if this is an "+
				"intentional algorithm/library change, bump Scheme and re-pin these goldens",
				seed, got, wantHex)
		}
	}
}
