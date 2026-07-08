package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/mode"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
)

type adultOllamaModelResponse struct {
	Model string `json:"model"`
}

type adultOllamaModelRequest struct {
	Model string `json:"model"`
}

// getAdultOllamaModelHandler returns the configured Adult Ollama model, or an
// empty string if none is set yet (unset is a normal state, not an error).
func getAdultOllamaModelHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		model, err := settingsStore.Get(r.Context(), mode.AdultOllamaModelKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(adultOllamaModelResponse{Model: model})
	}
}

// putAdultOllamaModelHandler stores the Adult Ollama model name.
func putAdultOllamaModelHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req adultOllamaModelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), mode.AdultOllamaModelKey, req.Model); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
