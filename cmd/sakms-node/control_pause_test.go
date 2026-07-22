//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbersanon/sakms/internal/apidto"
)

// pauseRecorder is a fake sakms server that records every node-auth pause push
// (PUT /api/nodes/{id}/pause) and replies with a configurable status.
type pauseRecorder struct {
	mu     sync.Mutex
	bodies []apidto.NodePauseRequest
	ids    []string
	auths  []string
	status int
}

func (p *pauseRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/nodes/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		var body apidto.NodePauseRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.ids = append(p.ids, r.PathValue("id"))
		p.auths = append(p.auths, r.Header.Get("Authorization"))
		st := p.status
		p.mu.Unlock()
		if st == 0 {
			st = http.StatusNoContent
		}
		w.WriteHeader(st)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (p *pauseRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodies)
}

func (p *pauseRecorder) last() apidto.NodePauseRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[len(p.bodies)-1]
}

// postPause posts to the /dispatch/pause control route and returns the status +
// decoded payload.
func postPause(t *testing.T, client *http.Client, paused bool) (int, dispatchPausePayload) {
	t.Helper()
	buf, _ := json.Marshal(dispatchPausePayload{Paused: paused})
	req, err := http.NewRequest(http.MethodPost, "http://unix/dispatch/pause", strings.NewReader(string(buf)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /dispatch/pause: %v", err)
	}
	defer resp.Body.Close()
	var out dispatchPausePayload
	json.NewDecoder(resp.Body).Decode(&out) //nolint:errcheck
	return resp.StatusCode, out
}

func getPause(t *testing.T, client *http.Client) dispatchPausePayload {
	t.Helper()
	resp, err := client.Get("http://unix/dispatch/pause")
	if err != nil {
		t.Fatalf("GET /dispatch/pause: %v", err)
	}
	defer resp.Body.Close()
	var out dispatchPausePayload
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding GET /dispatch/pause: %v", err)
	}
	return out
}

// readPersistedPause reads back the on-disk config.json's DispatchPaused bit.
func readPersistedPause(t *testing.T, configPath string) bool {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.json: %v", err)
	}
	var persisted NodeConfig
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("unmarshalling config.json: %v", err)
	}
	return persisted.DispatchPaused
}

// TestDispatchPause_GetReflectsConfig proves GET /dispatch/pause returns the
// daemon's cached DispatchPaused value.
func TestDispatchPause_GetReflectsConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: "http://unused.invalid", APIKey: "k", NodeName: "n", DispatchPaused: true}
	client, _, _ := startTestSocket(t, cfg, configPath)

	if got := getPause(t, client); !got.Paused {
		t.Fatalf("GET /dispatch/pause = %+v, want paused=true", got)
	}
}

// TestDispatchPause_SetPushesAndPersists proves POST /dispatch/pause flips the
// cached bit, persists it, and relays an authenticated PUT carrying the value.
func TestDispatchPause_SetPushesAndPersists(t *testing.T) {
	rec := &pauseRecorder{}
	srv := rec.server(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n"}
	sess := &nodeSession{}
	sess.setAck("node-abc", nil)
	client, _, _ := startTestSocketWith(t, cfg, configPath, newPathmapPusher(cfg, sess, http.DefaultClient, time.Hour), sess)

	status, out := postPause(t, client, true)
	if status != http.StatusOK {
		t.Fatalf("POST /dispatch/pause: status %d (%s)", status, out.Error)
	}
	if !out.Paused {
		t.Fatalf("response = %+v, want paused=true", out)
	}
	if !cfg.pauseSnapshot() {
		t.Fatal("cfg.DispatchPaused not flipped to true in-memory")
	}
	if !readPersistedPause(t, configPath) {
		t.Fatal("cfg.DispatchPaused not persisted to config.json")
	}
	if rec.count() != 1 {
		t.Fatalf("expected exactly one pause push, got %d", rec.count())
	}
	if body := rec.last(); !body.Paused {
		t.Fatalf("push body = %+v, want paused=true", body)
	}
	rec.mu.Lock()
	auth := rec.auths[0]
	id := rec.ids[0]
	rec.mu.Unlock()
	if auth != "Bearer k" {
		t.Fatalf("push Authorization = %q, want %q", auth, "Bearer k")
	}
	if id != "node-abc" {
		t.Fatalf("push URL id = %q, want %q (from ConnectAck)", id, "node-abc")
	}
}

// TestDispatchPause_FailedPushRollsBackToRunning is the critical rollback test:
// starting from the authoritative "running" state, a toggle to paused whose push
// FAILS (server 500) must roll the optimistic flip back to running — not leave a
// paused intent the server never accepted — and return a non-2xx + error so the
// tray can surface it via notify().
func TestDispatchPause_FailedPushRollsBackToRunning(t *testing.T) {
	rec := &pauseRecorder{status: http.StatusInternalServerError}
	srv := rec.server(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	// Authoritative starting state: running (DispatchPaused=false) — the last
	// server-echoed value the failed toggle must fall back to.
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", DispatchPaused: false}
	client, _, _ := startTestSocket(t, cfg, configPath)

	status, out := postPause(t, client, true)
	if status == http.StatusOK {
		t.Fatalf("expected a non-2xx on a failed push, got %d", status)
	}
	if out.Error == "" {
		t.Fatal("failed push must return a non-empty error for the tray to surface via notify()")
	}
	// The response reports the rolled-back authoritative value.
	if out.Paused {
		t.Fatalf("failed-push response = %+v, want the rolled-back paused=false", out)
	}
	// And the daemon's live + persisted state rolled back to running.
	if cfg.pauseSnapshot() {
		t.Fatal("failed push left DispatchPaused=true in-memory — did NOT roll back to the authoritative running state")
	}
	if readPersistedPause(t, configPath) {
		t.Fatal("failed push persisted DispatchPaused=true — the rollback was not saved")
	}
	// The push was actually attempted (the failure is a real server rejection,
	// not a skipped call).
	if rec.count() != 1 {
		t.Fatalf("expected the push to actually reach the server, got %d", rec.count())
	}
}

// TestDispatchPause_FailedPushRollsBackToPaused proves the rollback is symmetric:
// starting from the authoritative "paused" state, a toggle to running whose push
// fails rolls back to paused (not running).
func TestDispatchPause_FailedPushRollsBackToPaused(t *testing.T) {
	rec := &pauseRecorder{status: http.StatusInternalServerError}
	srv := rec.server(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &NodeConfig{ServerURL: srv.URL, APIKey: "k", NodeName: "n", DispatchPaused: true}
	client, _, _ := startTestSocket(t, cfg, configPath)

	status, out := postPause(t, client, false)
	if status == http.StatusOK {
		t.Fatalf("expected a non-2xx on a failed push, got %d", status)
	}
	if !out.Paused {
		t.Fatalf("failed-push response = %+v, want the rolled-back paused=true", out)
	}
	if !cfg.pauseSnapshot() {
		t.Fatal("failed push did NOT roll back to the authoritative paused state")
	}
	if !readPersistedPause(t, configPath) {
		t.Fatal("failed push did not persist the paused rollback")
	}
}
