package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/apidto"
	"github.com/curtiswtaylorjr/sakms/internal/downloader"
	"github.com/curtiswtaylorjr/sakms/internal/settings"
)

// Settings keys for the unified downloader's operator-tunable knobs.
const (
	DownloaderStagingDirKey     = "downloader_staging_dir"
	DownloaderMaxConcurrentKey  = "downloader_max_concurrent"
	DownloaderMaxConnectionsKey = "downloader_max_connections"
)

// Defaults for the concurrency knobs when unset (per the feature spec).
const (
	downloaderDefaultMaxConcurrent  = 3
	downloaderDefaultMaxConnections = 4
)

// toDTODownload maps a downloader.Download to the wire DTO, deriving a display
// filename (basename of the first file, GID fallback).
func toDTODownload(d downloader.Download) apidto.Download {
	name := d.Filename
	if name != "" {
		name = filepath.Base(name)
	}
	if name == "" {
		name = d.GID
	}
	return apidto.Download{
		GID:             d.GID,
		Status:          d.Status,
		Filename:        name,
		TotalLength:     d.TotalLength,
		CompletedLength: d.CompletedLength,
		DownloadSpeed:   d.DownloadSpeed,
		Connections:     d.Connections,
		ErrorMessage:    d.ErrorMessage,
	}
}

// mergedDownloads returns the full download queue as a DTO slice. Returns an
// empty (non-nil) slice when the queue is empty so JSON encodes [] not null.
func mergedDownloads(dl *downloader.Manager) []apidto.Download {
	list := dl.List()
	out := make([]apidto.Download, 0, len(list))
	for _, d := range list {
		out = append(out, toDTODownload(d))
	}
	return out
}

// listDownloadsHandler returns the current download queue.
func listDownloadsHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, mergedDownloads(dl))
	}
}

// downloadsStreamHandler streams the download queue as server-sent events,
// the same SSE shape the System Dashboard uses (see sysinfoStreamHandler):
// each event's data is a JSON array of the current downloads. It subscribes
// to the Manager's hub, so an event fires whenever the queue changes (a new
// download, progress, a completion), plus one immediately on connect so the
// UI paints without waiting for the first change.
func downloadsStreamHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ctx := r.Context()

		// Paint an initial snapshot immediately so the screen isn't blank until
		// the queue next changes.
		writeSSEData(w, flusher, mergedDownloads(dl))

		ch, cancel := dl.Subscribe()
		defer cancel()

		for {
			select {
			case <-ctx.Done():
				return
			case raw, ok := <-ch:
				if !ok {
					return
				}
				out := make([]apidto.Download, 0, len(raw))
				for _, d := range raw {
					out = append(out, toDTODownload(d))
				}
				writeSSEData(w, flusher, out)
			}
		}
	}
}

// writeSSEData marshals v and writes it as one SSE data frame, then flushes.
func writeSSEData(w http.ResponseWriter, flusher http.Flusher, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// cancelDownloadHandler cancels and removes a download.
func cancelDownloadHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		if err := dl.Cancel(r.PathValue("gid")); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// pauseDownloadHandler pauses an active download.
func pauseDownloadHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		if err := dl.Pause(r.PathValue("gid")); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// resumeDownloadHandler unpauses a paused download.
func resumeDownloadHandler(dl *downloader.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if dl == nil {
			http.Error(w, "the download engine isn't running", http.StatusServiceUnavailable)
			return
		}
		if err := dl.Resume(r.PathValue("gid")); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getDownloaderConfigHandler returns the downloader's staging dir + concurrency
// knobs, filling in defaults for unset numeric fields (staging dir "" when
// unset — the caller/boot supplies the real default path).
func getDownloaderConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		staging, err := getSetting(ctx, settingsStore, DownloaderStagingDirKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conc, err := getSettingInt(ctx, settingsStore, DownloaderMaxConcurrentKey, downloaderDefaultMaxConcurrent)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		conn, err := getSettingInt(ctx, settingsStore, DownloaderMaxConnectionsKey, downloaderDefaultMaxConnections)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, apidto.DownloaderConfig{
			StagingDir:     staging,
			MaxConcurrent:  conc,
			MaxConnections: conn,
		})
	}
}

// putDownloaderConfigHandler stores the downloader's staging dir + concurrency
// knobs. Concurrency values must be positive; staging dir is free-typed (it's
// validated for existence/writability the next time the engine restarts, same
// tolerance as a library root folder). A change takes effect on restart.
func putDownloaderConfigHandler(settingsStore *settings.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req apidto.DownloaderConfig
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.MaxConcurrent < 1 || req.MaxConnections < 1 {
			http.Error(w, "maxConcurrent and maxConnections must be at least 1", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if err := settingsStore.Set(ctx, DownloaderStagingDirKey, req.StagingDir); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, DownloaderMaxConcurrentKey, strconv.Itoa(req.MaxConcurrent)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := settingsStore.Set(ctx, DownloaderMaxConnectionsKey, strconv.Itoa(req.MaxConnections)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// getSetting returns a settings value, "" when unset (ErrNotFound is a normal
// "not configured" state, not an error).
func getSetting(ctx context.Context, store *settings.Store, key string) (string, error) {
	v, err := store.Get(ctx, key)
	if err != nil && !errors.Is(err, settings.ErrNotFound) {
		return "", err
	}
	return v, nil
}

// getSettingInt returns a settings value parsed as int, or def when unset or
// unparseable.
func getSettingInt(ctx context.Context, store *settings.Store, key string, def int) (int, error) {
	v, err := getSetting(ctx, store, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, nil
	}
	return n, nil
}
