package api

import (
	"testing"

	"github.com/labbersanon/sakms/internal/scanschedule"
)

// TestScanScheduleSettingsKeyParity guards the one seam scanschedule_settings.go's
// import-avoidance leaves unverified: its key constants are typed out by value
// rather than imported from internal/scanschedule (so this package still
// compiles, with the settings endpoints simply managing inert keys, if
// scanschedule is ever deleted — see scanschedule_settings.go's header
// comment). That means nothing at compile time stops the two copies from
// drifting apart, which would make the settings UI silently write a key the
// scheduler never reads. This test is the deliberate exception: it's fine for
// it to import scanschedule, because if scanschedule is ever deleted this
// file is deleted right alongside it, same as the production code it guards.
func TestScanScheduleSettingsKeyParity(t *testing.T) {
	cases := []struct {
		name     string
		apiKey   string
		schedKey string
	}{
		{"rename interval", renameScanIntervalKey, scanschedule.RenameIntervalKey},
		{"purge interval", purgeScanIntervalKey, scanschedule.PurgeIntervalKey},
		{"dedup interval", dedupScanIntervalKey, scanschedule.DedupIntervalKey},
		{"dedup vmaf enabled", dedupVMAFScanEnabledKey, scanschedule.DedupVMAFEnabledKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.apiKey != c.schedKey {
				t.Errorf("key drift: internal/api has %q, internal/scanschedule has %q", c.apiKey, c.schedKey)
			}
		})
	}
}
