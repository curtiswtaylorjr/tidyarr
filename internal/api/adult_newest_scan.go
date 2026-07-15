package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// adultNewestScanIntervalKey is the settings key for the background
// adultnewest scan cadence, in whole seconds (0 = off, the default).
// Mirrors recheckIntervalKey's import-avoidance rationale by value rather
// than importing internal/adultnewest's own copy of this constant, for the
// same reason: this endpoint's build shouldn't depend on that package.
const adultNewestScanIntervalKey = "adult_newest_scan_interval_seconds"

type adultNewestScanIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type adultNewestScanIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getAdultNewestScanIntervalHandler returns the configured scan interval in
// seconds, or 0 when unset — 0 is the normal "off" default (opt-in job, same
// convention as recheck). Mirrors getRecheckIntervalHandler exactly.
func getAdultNewestScanIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs := 0
		v, err := settingsStore.Get(r.Context(), adultNewestScanIntervalKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			secs = n
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adultNewestScanIntervalResponse{IntervalSeconds: secs})
	}
}

// putAdultNewestScanIntervalHandler stores the scan interval in seconds. 0
// disables the job; a negative value is rejected. Mirrors
// putRecheckIntervalHandler exactly.
func putAdultNewestScanIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adultNewestScanIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.IntervalSeconds < 0 {
			http.Error(w, "intervalSeconds must be zero (off) or a positive number of seconds", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), adultNewestScanIntervalKey, strconv.Itoa(req.IntervalSeconds)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
