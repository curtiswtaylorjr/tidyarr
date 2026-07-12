package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// recheckIntervalKey is the settings key for the background recheck cadence, in
// whole seconds (0 = off, the default). It intentionally mirrors
// recheck.IntervalSettingKey by value rather than importing internal/recheck:
// keeping internal/api free of any dependency on that deliberately-removable
// package means the recheck feature stays deletable by "delete the package +
// remove its one start-call in main" without breaking this endpoint's build
// (it would just manage an inert setting nothing reads). Same import-avoidance
// rationale as internal/availability's duplicated Newznab category codes.
const recheckIntervalKey = "recheck_interval_seconds"

type recheckIntervalResponse struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

type recheckIntervalRequest struct {
	IntervalSeconds int `json:"intervalSeconds"`
}

// getRecheckIntervalHandler returns the configured recheck interval in seconds,
// or 0 when unset — 0 is the normal "off" default (the background recheck job
// is opt-in), not an error. A stored-but-unparseable value degrades to 0 for
// the same reason recheck.LoadInterval does: a corrupt value means "off", not a
// 500.
func getRecheckIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secs := 0
		v, err := settingsStore.Get(r.Context(), recheckIntervalKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			secs = n
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(recheckIntervalResponse{IntervalSeconds: secs})
	}
}

// putRecheckIntervalHandler stores the recheck interval in seconds. 0 disables
// the job (the opt-in gate); a negative value is rejected. A change takes
// effect on the running loop's next tick if it's already enabled, or on next
// restart if it was off at boot (see recheck.Run's doc).
func putRecheckIntervalHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req recheckIntervalRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.IntervalSeconds < 0 {
			http.Error(w, "intervalSeconds must be zero (off) or a positive number of seconds", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), recheckIntervalKey, strconv.Itoa(req.IntervalSeconds)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
