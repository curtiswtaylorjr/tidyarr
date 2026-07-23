package api

import (
	"encoding/json"
	"net/http"

	"github.com/labbersanon/sakms/internal/settings"
)

// This file holds the GET/PUT settings endpoints for the general scan
// scheduler (internal/scanschedule): one interval per workflow plus the Dedup
// eager-VMAF toggle. Each key string is mirrored here by value rather than
// importing internal/scanschedule — the same import-avoidance recheck.go uses
// so the scheduler stays independently deletable (these endpoints would just
// manage inert settings nothing reads). Interval parse/degrade/validate logic
// is the shared loadIntervalSeconds/storeIntervalSeconds (interval.go); the
// bool uses the settings store directly.
const (
	renameScanIntervalKey   = "rename_scan_interval_seconds"
	purgeScanIntervalKey    = "purge_scan_interval_seconds"
	dedupScanIntervalKey    = "dedup_scan_interval_seconds"
	dedupVMAFScanEnabledKey = "dedup_vmaf_scan_enabled"

	// minScanIntervalSeconds is the floor storeIntervalSeconds enforces for
	// these keys only (recheck/adult-newest-scan pass 0, no floor) — see the
	// call site in putScanIntervalHandler for why a mutating-Scan scheduler
	// needs one and a read-only probe doesn't.
	minScanIntervalSeconds = 60
)

type scanIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type scanIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type dedupVMAFScanEnabledResponse struct {
	Enabled bool `json:"enabled"`
}

type dedupVMAFScanEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

// getScanIntervalHandler returns the configured interval-in-seconds for one
// scan-scheduler key (0 = off, the default). Off-by-default like recheck: a
// genuinely-unset or corrupt value reads as 0, not a 500.
func getScanIntervalHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs, err := loadIntervalSeconds(r.Context(), settingsStore, key, 0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(scanIntervalResponse{IntervalSeconds: secs})
	}
}

// putScanIntervalHandler stores an interval-in-seconds for one scan-scheduler
// key. 0 disables that workflow's schedule (the opt-in gate); a negative value
// is rejected. A change takes effect on the running loop's next tick if it was
// already enabled, or on next restart if it was off at boot (see
// scanschedule.Run's doc).
func putScanIntervalHandler(settingsStore *settings.Store, key string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scanIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		// 60s floor: unlike recheck/adult-newest-scan's read-only probes, a scan
		// cycle here triggers real Rename/Purge/Dedup Scan work (and, for Dedup,
		// eager VMAF) — an operator setting e.g. 1s would drive continuous
		// back-to-back scanning. AC15's skip-not-queue drain bounds the worst
		// case to "always running," not runaway goroutines, but a sane floor
		// prevents the misconfiguration outright rather than merely bounding it.
		badRequest, err := storeIntervalSeconds(r.Context(), settingsStore, key, req.IntervalSeconds, minScanIntervalSeconds)
		if err != nil {
			status := http.StatusInternalServerError
			if badRequest {
				status = http.StatusBadRequest
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getDedupVMAFScanEnabledHandler returns whether a scheduled Dedup cycle
// eagerly computes VMAF scores for the groups it finds (off by default).
func getDedupVMAFScanEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := settingsStore.GetBool(r.Context(), dedupVMAFScanEnabledKey, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(dedupVMAFScanEnabledResponse{Enabled: enabled})
	}
}

// putDedupVMAFScanEnabledHandler enables or disables eager VMAF on scheduled
// Dedup cycles. Independent of the Dedup scan interval (a schedule can run with
// or without eager VMAF).
func putDedupVMAFScanEnabledHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req dedupVMAFScanEnabledRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := settingsStore.SetBool(r.Context(), dedupVMAFScanEnabledKey, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
