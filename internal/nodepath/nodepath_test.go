package nodepath

import "testing"

// trivialCases is the canonical shared table both the server-side and node-side
// validators are expected to agree with (they call Trivial directly, so this is
// the single source of truth for "is this path too shallow to contain
// anything"). Node and server unit tests reuse these same classes to prove both
// layers reject/accept identically.
var trivialCases = []struct {
	path string
	want bool
}{
	{"", true},
	{"   ", true},
	{"/", true},
	{"//", true},
	{".", true},
	{"/mnt", true},
	{"/mnt/", true},
	{"/mnt/.", true},
	{"mnt", true}, // single relative segment
	{"/mnt/media", false},
	{"/mnt/media/", false},
	{"/mnt/media/movies", false},
	{"/srv/tank/movies/kids", false},
}

func TestTrivial(t *testing.T) {
	for _, tc := range trivialCases {
		if got := Trivial(tc.path); got != tc.want {
			t.Errorf("Trivial(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestMinDepthIsTwo(t *testing.T) {
	// The whole safeguard rests on requiring at least two segments; a regression
	// to 1 would let "/mnt" satisfy the required-mediaRoot gate (D9).
	if MinDepth != 2 {
		t.Fatalf("MinDepth = %d, want 2", MinDepth)
	}
}
