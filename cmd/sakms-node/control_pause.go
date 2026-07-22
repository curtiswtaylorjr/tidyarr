//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// dispatchPausePayload is the request/response body for the /dispatch/pause
// control routes. A request carries Paused; a response echoes the resulting
// display value (and, on a failed push, the rolled-back value plus an Error).
type dispatchPausePayload struct {
	Paused bool   `json:"paused"`
	Error  string `json:"error,omitempty"`
}

// registerPauseRoutes wires the node-pause-dispatch Stage 3 control routes onto
// mux. Same group-gated, browser-unreachable socket as the mediaRoots/pathmap
// routes. The push HTTP client is built here and closed over by the POST handler
// — a single synchronous toggle needs no debounce/coalescing (unlike the pathmap
// pusher), so there is no cross-reconnect state to hoist into main().
func registerPauseRoutes(mux *http.ServeMux, cfg *NodeConfig, configPath string, sess *nodeSession) {
	client := &http.Client{Timeout: 30 * time.Second}
	mux.HandleFunc("GET /dispatch/pause", func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(w, http.StatusOK, dispatchPausePayload{Paused: cfg.pauseSnapshot()})
	})
	mux.HandleFunc("POST /dispatch/pause", func(w http.ResponseWriter, r *http.Request) {
		handleDispatchPauseSet(w, r, cfg, configPath, sess, client)
	})
}

// handleDispatchPauseSet flips the node's display-only pause bit and relays the
// toggle to the server over the node's bearer identity.
//
// Order (node-pause-dispatch Stage 3):
//  1. Optimistically flip cfg.DispatchPaused for instant tray feedback, CAPTURING
//     the prior value first — that prior value is the last server-echoed
//     authoritative state, the only honest thing to fall back to.
//  2. Relay an authenticated PUT /api/nodes/{id}/pause to the server (synchronous;
//     a single toggle needs no debounce). The server persists it, applies the
//     live dispatch effect, and echoes the authoritative value back over SSE.
//  3. On a FAILED push, roll the optimistic flip BACK to the captured value and
//     return a non-2xx with an Error so the tray surfaces it via notify(). This
//     deliberately differs from the pathmap feature's "retain last-known-good":
//     a pause toggle whose push failed has no valid new state worth keeping — the
//     honest display is the pre-toggle authoritative value.
//
// The HTTP round trip never runs under cfg.mu (the deadlock discipline): the flip
// and the rollback are each their own mutateAndSave critical section, with the
// network I/O strictly between them.
func handleDispatchPauseSet(w http.ResponseWriter, r *http.Request, cfg *NodeConfig, configPath string, sess *nodeSession, client *http.Client) {
	var req dispatchPausePayload
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			rejectPause(w, r, fmt.Errorf("decoding request body: %w", err))
			return
		}
	}

	// Optimistic flip, capturing the prior (last-echoed authoritative) value.
	var prev bool
	if saveErr := cfg.mutateAndSave(configPath, func() {
		prev = cfg.DispatchPaused
		cfg.DispatchPaused = req.Paused
	}); saveErr != nil {
		rejectPause(w, r, saveErr)
		return
	}

	if err := pushPause(r.Context(), cfg, sess, client, req.Paused); err != nil {
		// Roll back to the captured authoritative value so the tray never shows an
		// intent the server never accepted.
		if saveErr := cfg.mutateAndSave(configPath, func() {
			cfg.DispatchPaused = prev
		}); saveErr != nil {
			log.Printf("sakms-node: control socket: rolling back pause after failed push: %v", saveErr)
		}
		log.Printf("sakms-node: control socket: dispatch pause push failed (rolled back to %v) (%s): %v", prev, peerUID(r.Context()), err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(dispatchPausePayload{Paused: prev, Error: err.Error()}) //nolint:errcheck
		return
	}

	log.Printf("sakms-node: control socket: dispatch pause set to %v (%s); relayed to server", req.Paused, peerUID(r.Context()))
	writeJSONStatus(w, http.StatusOK, dispatchPausePayload{Paused: req.Paused})
}

// pushPause relays the toggle to the server's dedicated dual-auth pause endpoint
// using the node's bearer identity, mirroring pathmap_push.go's push transport.
// The server keys the write by the authenticated bearer identity and ignores the
// URL {id} (D2), so the id is a route-pattern formality — "self" before the first
// connect.
func pushPause(ctx context.Context, cfg *NodeConfig, sess *nodeSession, client *http.Client, paused bool) error {
	serverURL, apiKey := cfg.transport()

	buf, err := json.Marshal(apidto.NodePauseRequest{Paused: paused})
	if err != nil {
		return fmt.Errorf("marshalling pause: %w", err)
	}

	id := sess.id()
	if id == "" {
		id = "self"
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPut, serverURL+"/api/nodes/"+id+"/pause", bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("building pause request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending pause: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server rejected pause: status %d", resp.StatusCode)
	}
	return nil
}

// rejectPause mirrors rejectPathMap: logs the daemon-side rejection attributed to
// the peer uid and returns 400 with a JSON error body.
func rejectPause(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("sakms-node: control socket: rejected dispatch pause (%s): %v", peerUID(r.Context()), err)
	writeJSONStatus(w, http.StatusBadRequest, dispatchPausePayload{Error: err.Error()})
}
