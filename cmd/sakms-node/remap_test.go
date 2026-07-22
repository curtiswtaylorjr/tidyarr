package main

import (
	"bytes"
	"log"
	"sort"
	"strings"
	"testing"

	"github.com/labbersanon/sakms/internal/nodes"
)

func TestRemap(t *testing.T) {
	cases := []struct {
		name    string
		entries []PathMapEntry
		input   string
		want    string
	}{
		{
			name: "linux to linux",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "linux to macOS style",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/Volumes/Adult-NAS"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/Volumes/Adult-NAS/foo.mkv",
		},
		{
			name: "linux to windows style",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: `Z:\Adult-NAS`},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  `Z:\Adult-NAS\foo.mkv`,
		},
		{
			name: "no match returns original",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Movies/foo.mkv",
			want:  "/mnt/Movies/foo.mkv",
		},
		{
			name: "longest prefix wins",
			entries: []PathMapEntry{
				{Server: "/mnt", Local: "/short"},
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "longest prefix wins reversed entry order",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
				{Server: "/mnt", Local: "/short"},
			},
			input: "/mnt/Adult-NAS/foo.mkv",
			want:  "/data/Adult/foo.mkv",
		},
		{
			name: "boundary: /mnt must not match /mnt-other",
			entries: []PathMapEntry{
				{Server: "/mnt", Local: "/local"},
			},
			input: "/mnt-other/foo.mkv",
			want:  "/mnt-other/foo.mkv",
		},
		{
			name: "exact prefix match (no trailing path)",
			entries: []PathMapEntry{
				{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
			},
			input: "/mnt/Adult-NAS",
			want:  "/data/Adult",
		},
		{
			name:    "empty entries returns original",
			entries: nil,
			input:   "/mnt/foo.mkv",
			want:    "/mnt/foo.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Remap(tc.entries, tc.input)
			if got != tc.want {
				t.Errorf("Remap(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestRemapLogsWarningOnNoMatch confirms the "no mapping matched" case (the
// Stage 0 silent-failure fix) always logs a WARN naming the unmapped
// serverPath and the configured prefixes that were checked, instead of
// silently returning serverPath unchanged.
func TestRemapLogsWarningOnNoMatch(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	entries := []PathMapEntry{
		{Server: "/mnt/Adult-NAS", Local: "/data/Adult"},
		{Server: "/mnt/Movies-NAS", Local: "/data/Movies"},
	}
	got := Remap(entries, "/mnt/Series-NAS/foo.mkv")
	if got != "/mnt/Series-NAS/foo.mkv" {
		t.Fatalf("Remap returned %q, want unchanged serverPath", got)
	}

	logged := buf.String()
	if !strings.Contains(logged, "WARNING") {
		t.Fatalf("expected a WARNING log on no-match, got: %q", logged)
	}
	if !strings.Contains(logged, "/mnt/Series-NAS/foo.mkv") {
		t.Fatalf("expected the log to name the unmapped serverPath, got: %q", logged)
	}
	if !strings.Contains(logged, "/mnt/Adult-NAS") || !strings.Contains(logged, "/mnt/Movies-NAS") {
		t.Fatalf("expected the log to name the configured prefixes, got: %q", logged)
	}
}

// TestRemapNoWarningOnMatch confirms a successful match does NOT log a
// no-mapping warning — the log is specific to the silent-failure branch.
func TestRemapNoWarningOnMatch(t *testing.T) {
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOut)

	entries := []PathMapEntry{{Server: "/mnt/Adult-NAS", Local: "/data/Adult"}}
	Remap(entries, "/mnt/Adult-NAS/foo.mkv")

	if buf.Len() != 0 {
		t.Fatalf("expected no log output on a successful match, got: %q", buf.String())
	}
}

// sortedByServer returns a copy of entries sorted by Server, so tests can
// compare map-derived (unordered) results deterministically.
func sortedByServer(entries []PathMapEntry) []PathMapEntry {
	out := append([]PathMapEntry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Server < out[j].Server })
	return out
}

func TestMergePathMap(t *testing.T) {
	t.Run("incoming key not in existing is added", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies"}}
		incoming := []nodes.PathMapping{{Server: "/mnt/series", Local: "/data/series"}}
		got := sortedByServer(mergePathMap(existing, incoming))
		want := []PathMapEntry{
			{Server: "/mnt/movies", Local: "/data/movies"},
			{Server: "/mnt/series", Local: "/data/series"},
		}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("incoming key already in existing is replaced", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/old/movies"}}
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/new/movies"}}
		got := mergePathMap(existing, incoming)
		if len(got) != 1 || got[0].Local != "/new/movies" {
			t.Fatalf("got %+v, want one entry with Local=/new/movies", got)
		}
	})

	t.Run("existing key NOT in incoming is left untouched — the core merge guarantee", func(t *testing.T) {
		existing := []PathMapEntry{
			{Server: "/mnt/movies", Local: "/data/movies"},
			{Server: "/mnt/adult", Local: "/data/adult"},
		}
		// incoming only carries movies — adult's row is unconfigured/disabled
		// on the server side and was never included in the push.
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/data/movies-v2"}}
		got := sortedByServer(mergePathMap(existing, incoming))
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2 (adult's entry must survive)", len(got))
		}
		if got[0].Server != "/mnt/adult" || got[0].Local != "/data/adult" {
			t.Errorf("adult entry changed: got %+v, want unchanged {/mnt/adult /data/adult}", got[0])
		}
		if got[1].Server != "/mnt/movies" || got[1].Local != "/data/movies-v2" {
			t.Errorf("movies entry: got %+v, want updated {/mnt/movies /data/movies-v2}", got[1])
		}
	})

	t.Run("empty incoming leaves existing entirely untouched", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies"}}
		got := mergePathMap(existing, nil)
		if len(got) != 1 || got[0] != existing[0] {
			t.Fatalf("got %+v, want existing unchanged: %+v", got, existing)
		}
	})

	t.Run("empty existing just adopts incoming (fresh node case)", func(t *testing.T) {
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/data/movies"}}
		got := mergePathMap(nil, incoming)
		if len(got) != 1 || got[0].Server != "/mnt/movies" || got[0].Local != "/data/movies" {
			t.Fatalf("got %+v, want one entry matching incoming", got)
		}
	})
}

// TestKeyIsInert_RemapAndMergeUnchanged proves the additive PathMapEntry.Key /
// PathMapping.Key field is genuinely inert: neither Remap's longest-Server-prefix
// matching nor mergePathMap's add/replace-by-Server dedup keys off it, so the
// safety-relevant matching/keying behavior is identical whether or not Key is
// populated (D7's add/replace-by-Server invariant is untouched).
func TestKeyIsInert_RemapAndMergeUnchanged(t *testing.T) {
	t.Run("Remap ignores Key entirely", func(t *testing.T) {
		withoutKey := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies"}}
		withKey := []PathMapEntry{{Server: "/mnt/movies", Local: "/data/movies", Key: "movies_library_root_folder"}}
		gotNoKey := Remap(withoutKey, "/mnt/movies/foo.mkv")
		gotKey := Remap(withKey, "/mnt/movies/foo.mkv")
		if gotNoKey != gotKey {
			t.Fatalf("Remap diverged on Key presence: without=%q with=%q", gotNoKey, gotKey)
		}
		if gotKey != "/data/movies/foo.mkv" {
			t.Fatalf("Remap = %q, want /data/movies/foo.mkv", gotKey)
		}
	})

	t.Run("mergePathMap dedups by Server, NOT by Key", func(t *testing.T) {
		// Two incoming entries share a Server but carry DIFFERENT Keys. If merge
		// keyed off Key, this would yield two rows; keyed off Server (correct),
		// the later one wins and there is exactly one row — proving Key plays no
		// part in the dedup key.
		incoming := []nodes.PathMapping{
			{Server: "/mnt/movies", Local: "/data/first", Key: "movies_library_root_folder"},
			{Server: "/mnt/movies", Local: "/data/second", Key: "series_library_root_folder"},
		}
		got := mergePathMap(nil, incoming)
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1 (dedup must be by Server, not Key): %+v", len(got), got)
		}
		if got[0].Server != "/mnt/movies" || got[0].Local != "/data/second" {
			t.Errorf("got %+v, want the last same-Server entry to win by Server", got[0])
		}
	})

	t.Run("replace-by-Server holds even when Keys differ", func(t *testing.T) {
		existing := []PathMapEntry{{Server: "/mnt/movies", Local: "/old", Key: "movies_library_root_folder"}}
		// Incoming carries the same Server with a different Key — still a replace
		// (Server match), and the new Key/Local ride through as the merged value.
		incoming := []nodes.PathMapping{{Server: "/mnt/movies", Local: "/new", Key: "adult_library_root_folder"}}
		got := mergePathMap(existing, incoming)
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1 (replace by Server): %+v", len(got), got)
		}
		if got[0].Local != "/new" || got[0].Key != "adult_library_root_folder" {
			t.Errorf("got %+v, want replaced {Local:/new Key:adult_library_root_folder}", got[0])
		}
	})
}
