package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
)

type ollamaModelResponse struct {
	Model string `json:"model"`
}

type ollamaModelRequest struct {
	Model string `json:"model"`
}

// getOllamaModelHandler returns the configured Ollama model stored under
// settingsKey, or an empty string if none is set yet (unset is a normal
// state, not an error). Shared by every settings-backed Ollama model
// endpoint — Adult's and Mainstream's alike, since both are the same "one
// scalar string" shape.
func getOllamaModelHandler(settingsStore *settings.Store, settingsKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		model, err := settingsStore.Get(r.Context(), settingsKey)
		if err != nil && !errors.Is(err, settings.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ollamaModelResponse{Model: model})
	}
}

// putOllamaModelHandler stores the Ollama model name under settingsKey.
func putOllamaModelHandler(settingsStore *settings.Store, settingsKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ollamaModelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Model == "" {
			http.Error(w, "model is required", http.StatusBadRequest)
			return
		}
		if err := settingsStore.Set(r.Context(), settingsKey, req.Model); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
