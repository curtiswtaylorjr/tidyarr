package downloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/aria2"
)

// fakeAria2 is a minimal JSON-RPC server that returns a scripted tellActive/
// tellStopped result, so the Manager's poll loop and onComplete callback can
// be driven without a real aria2c subprocess.
type fakeAria2 struct {
	mu      sync.Mutex
	stopped []map[string]any // what tellStopped returns
	active  []map[string]any // what tellActive returns
}

func (f *fakeAria2) setActive(a []map[string]any)  { f.mu.Lock(); f.active = a; f.mu.Unlock() }
func (f *fakeAria2) setStopped(s []map[string]any) { f.mu.Lock(); f.stopped = s; f.mu.Unlock() }

func (f *fakeAria2) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var result any
		switch req.Method {
		case "aria2.tellActive":
			result = f.active
		case "aria2.tellStopped":
			result = f.stopped
		case "aria2.tellWaiting":
			result = []map[string]any{}
		default:
			result = "ok"
		}
		raw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(map[string]any{"result": json.RawMessage(raw)})
	}
}

// newTestManager builds a Manager whose RPC client points at srv (never
// launching a real subprocess — Start is not called in these tests; the poll
// loop is driven directly).
func newTestManager(t *testing.T, srvURL string, onComplete func(string, []string)) *Manager {
	t.Helper()
	m := &Manager{
		rpc:         aria2.New(aria2.Config{Endpoint: srvURL}, http.DefaultClient),
		http:        http.DefaultClient,
		onComplete:  onComplete,
		subscribers: map[int]chan []aria2.Download{},
		lastByGID:   map[string]seen{},
	}
	return m
}

func TestManager_OnCompleteFiresOncePerCompletion(t *testing.T) {
	fake := &fakeAria2{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	var mu sync.Mutex
	var completedGIDs []string
	var completedFiles [][]string
	done := make(chan struct{}, 1)
	m := newTestManager(t, srv.URL, func(gid string, files []string) {
		mu.Lock()
		completedGIDs = append(completedGIDs, gid)
		completedFiles = append(completedFiles, files)
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Start active, then complete: the item first appears active, then moves
	// to stopped as "complete" — onComplete should fire exactly once.
	fake.setActive([]map[string]any{{
		"gid": "abc", "status": "active", "totalLength": "100", "completedLength": "50",
		"files": []map[string]any{{"path": "/staging/movie.mkv"}},
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.pollLoop(ctx)

	// Let a couple of active polls happen (no completion yet).
	time.Sleep(2 * pollInterval)
	mu.Lock()
	if len(completedGIDs) != 0 {
		mu.Unlock()
		t.Fatalf("onComplete fired before completion: %v", completedGIDs)
	}
	mu.Unlock()

	// Now the download completes: gone from active, present in stopped as complete.
	fake.setActive(nil)
	fake.setStopped([]map[string]any{{
		"gid": "abc", "status": "complete", "totalLength": "100", "completedLength": "100",
		"files": []map[string]any{{"path": "/staging/movie.mkv"}},
	}})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("onComplete never fired after completion")
	}

	// Poll several more times — it must NOT fire again for the same GID.
	time.Sleep(3 * pollInterval)
	mu.Lock()
	defer mu.Unlock()
	if len(completedGIDs) != 1 {
		t.Fatalf("onComplete fired %d times, want exactly 1: %v", len(completedGIDs), completedGIDs)
	}
	if completedGIDs[0] != "abc" {
		t.Errorf("completed gid = %q, want abc", completedGIDs[0])
	}
	if len(completedFiles[0]) != 1 || completedFiles[0][0] != "/staging/movie.mkv" {
		t.Errorf("completed files = %v", completedFiles[0])
	}
}

func TestManager_SubscribeReceivesSnapshotOnChange(t *testing.T) {
	fake := &fakeAria2{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	m := newTestManager(t, srv.URL, nil)
	ch, cancel := m.Subscribe()
	defer cancel()

	fake.setActive([]map[string]any{{
		"gid": "g1", "status": "active", "totalLength": "100", "completedLength": "10",
		"files": []map[string]any{{"path": "/staging/a.mkv"}},
	}})

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go m.pollLoop(ctx)

	select {
	case snap := <-ch:
		if len(snap) != 1 || snap[0].GID != "g1" {
			t.Fatalf("snapshot = %v, want one download g1", snap)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscriber never received a snapshot")
	}
}

func TestManager_UnsubscribeClosesChannel(t *testing.T) {
	m := newTestManager(t, "http://unused", nil)
	ch, cancel := m.Subscribe()
	cancel()
	// A cancelled subscription's channel must be closed (receive returns
	// zero-value, ok=false).
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel delivered a value after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Error("channel not closed after unsubscribe")
	}
}
