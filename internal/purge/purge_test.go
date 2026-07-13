package purge

import (
	"testing"
)

// The full curated allowlist, mirroring stash-whisparr-sort's
// internal/config.PurgeTagAllowlist — kept as a local literal here so this
// test doesn't depend on any config package, matching purge's original
// leaf-level design.
var allowlist = []string{
	"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
	"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
	"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
	"Transgender (Female)", "Twosome (Trans)",
	"Bound", "Bound Wrists", "Bound Arms", "Bound Legs", "Chained", "Rope",
	"Crotch Rope", "Shibari", "Ribbon Bondage", "Breast Bondage", "Ball Gag",
	"Bit Gag", "Tape Gag", "Improvised Gag", "Whip", "Slave", "Dominatrix",
	"Spiked Collar", "Metal Collar", "Animal Collar",
	"Shemale", "She-male", "Chicks with Dicks", "Trannies", "Tgirls", "T-Girl",
	"Transmasculine", "Trans Women", "Trans Men", "Transgender Erotica",
	"FTM Gay Porn", "Queer Porn", "Feminist Porn", "Nonbinary", "Genderqueer",
	"Gender Variant Media",
	"Futanari", "Futa with Female", "Futa with Male", "Implied Futanari",
	"Crossdressing",
}

func TestMatchesAny_AllKnownLiveTagsStillMatch(t *testing.T) {
	known := []string{
		"BDSM", "Bondage", "Bondage Blowjob", "Bondage Collar", "Bondage Sex",
		"Dungeon", "Latina Trans", "Trans Fucked by Female", "Trans Fucked by Male",
		"Trans Fucks Female", "Trans Fucks Male", "Trans Fucks Trans", "Transgender",
		"Transgender (Female)", "Twosome (Trans)",
	}
	for _, tag := range known {
		t.Run(tag, func(t *testing.T) {
			if !MatchesAny(tag, allowlist) {
				t.Errorf("expected %q to match (regression against live data)", tag)
			}
		})
	}
}

// Transgender and Transformation are the case that breaks word-boundary
// regex matching (see the package doc comment) — exact matching has no such
// ambiguity.
func TestMatchesAny_TransgenderVsTransformation(t *testing.T) {
	if !MatchesAny("Transgender", allowlist) {
		t.Fatal("Transgender must match — it's an explicit allowlist entry")
	}
	if MatchesAny("Transformation", allowlist) {
		t.Fatal("Transformation must NOT match — not in the allowlist, and exact matching has no substring ambiguity")
	}
}

func TestMatchesAny_UnrelatedTagsNeverMatch(t *testing.T) {
	cases := []string{
		"Transformation", "Transatlantic", "Translator", "Transcript",
		"Bondage-Free", "Vanilla Romance", "Blonde", "Anal", "Threesome",
		"Chainsaw", "Collarbone", "Sailor Collar",
	}
	for _, tag := range cases {
		t.Run(tag, func(t *testing.T) {
			if MatchesAny(tag, allowlist) {
				t.Errorf("expected %q NOT to match — not an allowlist entry", tag)
			}
		})
	}
}

func TestMatchesAny_CaseInsensitive(t *testing.T) {
	if !MatchesAny("bdsm", allowlist) {
		t.Fatal("expected case-insensitive match for lowercase 'bdsm'")
	}
	if !MatchesAny("SHEMALE", allowlist) {
		t.Fatal("expected case-insensitive match for uppercase 'SHEMALE'")
	}
}

func TestMatchedEntries_ReportsWhichRuleFired(t *testing.T) {
	got := MatchedEntries("latina trans", allowlist) // case-insensitive input
	if len(got) != 1 || got[0] != "Latina Trans" {
		t.Fatalf("expected [\"Latina Trans\"], got %v", got)
	}
}

func TestMatchedEntries_NoMatch(t *testing.T) {
	got := MatchedEntries("Vanilla", allowlist)
	if len(got) != 0 {
		t.Fatalf("expected no matches, got %v", got)
	}
}
