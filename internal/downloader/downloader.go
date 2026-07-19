// Package downloader manages SAK's unified download engine: a single aria2c
// subprocess plus a JSON-RPC client (internal/aria2) and a subscriber hub
// that fans out live download-queue snapshots for the Downloads screen's SSE
// stream.
//
// DELIBERATE, opt-in exception to this project's "manual by default, no
// background pollers" convention (CLAUDE.md): the Manager runs one background
// goroutine that polls aria2 every pollInterval and, on a completed download,
// fires an onComplete callback that runs the auto-import. This is the same
// kind of documented exception as internal/recheck / internal/adultnewest /
// watch-folders — a download engine inherently needs to observe its
// subprocess's progress; there's no human-triggered equivalent of "the
// download finished."
//
// Lifetime: a Manager owns a subprocess and long-lived goroutines, so it is a
// PROCESS-LIFETIME SINGLETON — constructed once in cmd/sakms/main.go and
// started with `go m.Start(ctx)` alongside the other background jobs, never
// per-request (unlike mode.Session's cheap per-request clients). The same
// pointer is injected wherever a grab needs to reach the RPC client.
//
// Import discipline: this package imports only internal/aria2 + stdlib — NOT
// mode/grabs/library — so it never forms an import cycle with mode.Session
// (which references *Manager). The onComplete callback is a plain
// func(gid string, files []string) set at construction, closing over whatever
// stores the caller needs in main.go.
package downloader

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/aria2"
)

// pollInterval is how often the Manager's background loop re-reads aria2's
// active/waiting/stopped lists to detect changes and completions.
const pollInterval = 750 * time.Millisecond

// maxBackoff caps the exponential restart backoff after aria2c exits.
const maxBackoff = 30 * time.Second

// Config parameterizes the Manager.
type Config struct {
	BinaryPath string // path to the extracted aria2c binary
	StagingDir string // aria2c's download directory
	RPCPort    int    // JSON-RPC port (default 6800)
	RPCToken   string // aria2 --rpc-secret, from internal/secrets
	MaxConc    int    // --max-concurrent-downloads
	MaxConn    int    // --max-connection-per-server
}

// Manager owns the aria2c subprocess, its RPC client, and the SSE hub.
type Manager struct {
	cfg  Config
	rpc  *aria2.Client
	http *http.Client

	// onComplete fires once per GID that transitions into "complete", with
	// the GID and the resolved file paths aria2 reported. Set at construction
	// (may be nil in tests that don't exercise import). It closes over the
	// grabs/library stores in main.go — kept as a plain func so this package
	// never imports them.
	onComplete func(gid string, files []string)

	mu          sync.Mutex
	subscribers map[int]chan []aria2.Download
	nextSubID   int
	// lastByGID remembers each GID's last-seen (status, completedLength) so
	// the loop can diff snapshots and only fan out on a real change, and fire
	// onComplete exactly once per completion.
	lastByGID map[string]seen
}

type seen struct {
	status    string
	completed int64
}

// New builds a Manager. onComplete may be nil (e.g. in tests). The RPC
// endpoint is derived from cfg.RPCPort; the client is built immediately so
// RPC() is usable, though calls fail until Start has the subprocess up.
func New(cfg Config, httpClient *http.Client) *Manager {
	port := cfg.RPCPort
	if port == 0 {
		port = 6800
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port) + "/jsonrpc"
	return &Manager{
		cfg:         cfg,
		rpc:         aria2.New(aria2.Config{Endpoint: endpoint, Token: cfg.RPCToken}, httpClient),
		http:        httpClient,
		subscribers: map[int]chan []aria2.Download{},
		lastByGID:   map[string]seen{},
	}
}

// NewForTesting builds a Manager whose RPC client points at an arbitrary
// endpoint (a fake aria2 JSON-RPC httptest server) with the given staging dir,
// WITHOUT deriving the endpoint from a port. It exists so tests in other
// packages (e.g. internal/api's grab/check-import handlers) can exercise the
// full download path against a fake aria2 without launching a real subprocess.
// Start must NOT be called on a Manager built this way — there's no real
// binary to run.
func NewForTesting(rpcEndpoint, stagingDir string, httpClient *http.Client) *Manager {
	return &Manager{
		cfg:         Config{StagingDir: stagingDir},
		rpc:         aria2.New(aria2.Config{Endpoint: rpcEndpoint}, httpClient),
		http:        httpClient,
		subscribers: map[int]chan []aria2.Download{},
		lastByGID:   map[string]seen{},
	}
}

// SetOnComplete wires the completion callback after construction — used from
// main.go, where the callback needs stores that are built alongside the
// Manager. Safe to call before Start.
func (m *Manager) SetOnComplete(fn func(gid string, files []string)) {
	m.onComplete = fn
}

// RPC returns the aria2 JSON-RPC client — the grab/download handlers call
// through this to add/pause/remove downloads.
func (m *Manager) RPC() *aria2.Client { return m.rpc }

// StagingDir is where aria2c writes downloads — passed as the per-item dir on
// AddTorrent so every grab stages under one known root the import step reads
// from.
func (m *Manager) StagingDir() string { return m.cfg.StagingDir }

// Start launches the aria2c subprocess and the poll loop, restarting the
// subprocess with exponential backoff on a non-zero exit, until ctx is
// cancelled. Blocks until then. Intended to run as `go m.Start(ctx)`.
func (m *Manager) Start(ctx context.Context) error {
	if m.cfg.StagingDir != "" {
		if err := os.MkdirAll(m.cfg.StagingDir, 0o755); err != nil {
			log.Printf("downloader: creating staging dir %s: %v", m.cfg.StagingDir, err)
		}
	}

	go m.pollLoop(ctx)

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		err := m.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			log.Printf("downloader: aria2c exited: %v", err)
		} else {
			log.Printf("downloader: aria2c exited cleanly, restarting")
		}
		// A process that stayed up a healthy while resets the backoff; a
		// rapid crash-loop escalates it toward maxBackoff.
		if time.Since(start) > maxBackoff {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// runOnce launches aria2c once and blocks until it exits (or ctx is
// cancelled, which kills it via CommandContext).
func (m *Manager) runOnce(ctx context.Context) error {
	args := []string{
		"--enable-rpc",
		"--rpc-listen-all=false",
		"--rpc-listen-port=" + strconv.Itoa(m.rpcPort()),
		"--continue=true",
		"--dir=" + m.cfg.StagingDir,
	}
	if m.cfg.RPCToken != "" {
		args = append(args, "--rpc-secret="+m.cfg.RPCToken)
	}
	if m.cfg.MaxConc > 0 {
		args = append(args, "--max-concurrent-downloads="+strconv.Itoa(m.cfg.MaxConc))
	}
	if m.cfg.MaxConn > 0 {
		args = append(args, "--max-connection-per-server="+strconv.Itoa(m.cfg.MaxConn))
	}

	cmd := exec.CommandContext(ctx, m.cfg.BinaryPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go forwardLogs(stdout)
	go forwardLogs(stderr)
	return cmd.Wait()
}

func (m *Manager) rpcPort() int {
	if m.cfg.RPCPort == 0 {
		return 6800
	}
	return m.cfg.RPCPort
}

// forwardLogs relays a subprocess pipe to the standard log, line by line.
func forwardLogs(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		log.Printf("aria2c: %s", sc.Text())
	}
}

// Subscribe registers a new SSE subscriber. It returns a receive channel that
// gets every subsequent queue snapshot, and a cancel func that unsubscribes
// and closes the channel. The channel is buffered by 1 and drops the oldest
// pending snapshot on a slow consumer, so a stalled subscriber never blocks
// the poll loop.
func (m *Manager) Subscribe() (<-chan []aria2.Download, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextSubID
	m.nextSubID++
	ch := make(chan []aria2.Download, 1)
	m.subscribers[id] = ch
	cancel := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if c, ok := m.subscribers[id]; ok {
			delete(m.subscribers, id)
			close(c)
		}
	}
	return ch, cancel
}

// pollLoop reads aria2's active+waiting+stopped lists every pollInterval,
// fans out a merged snapshot to subscribers on any change, and fires
// onComplete once per newly-completed GID.
func (m *Manager) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var prev []aria2.Download
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := m.snapshot(ctx)
			if err != nil {
				// aria2 not up yet, or a transient RPC failure — skip this
				// tick rather than tearing anything down.
				continue
			}
			m.detectCompletions(snap)
			if !sameSnapshot(prev, snap) {
				m.fanout(snap)
				prev = snap
			}
		}
	}
}

// snapshot merges active + waiting + a bounded recent-stopped window into one
// ordered list (active first, then waiting, then stopped).
func (m *Manager) snapshot(ctx context.Context) ([]aria2.Download, error) {
	active, err := m.rpc.TellActive(ctx)
	if err != nil {
		return nil, err
	}
	waiting, err := m.rpc.TellWaiting(ctx, 0, 100)
	if err != nil {
		return nil, err
	}
	stopped, err := m.rpc.TellStopped(ctx, 0, 100)
	if err != nil {
		return nil, err
	}
	out := make([]aria2.Download, 0, len(active)+len(waiting)+len(stopped))
	out = append(out, active...)
	out = append(out, waiting...)
	out = append(out, stopped...)
	return out, nil
}

// detectCompletions fires onComplete once per GID that has newly transitioned
// into "complete" since the last poll, then records every GID's latest state.
func (m *Manager) detectCompletions(snap []aria2.Download) {
	for _, d := range snap {
		prev, existed := m.lastByGID[d.GID]
		if d.Status == "complete" && (!existed || prev.status != "complete") {
			if m.onComplete != nil {
				// Run in a goroutine so a slow import (relocate + library
				// upsert + player notify) never stalls the poll loop.
				gid, files := d.GID, append([]string(nil), d.Files...)
				go m.onComplete(gid, files)
			}
		}
		m.lastByGID[d.GID] = seen{status: d.Status, completed: d.CompletedLength}
	}
}

// fanout delivers snap to every subscriber, dropping a stale pending snapshot
// for any subscriber whose buffer is full (latest-wins, never blocks).
func (m *Manager) fanout(snap []aria2.Download) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subscribers {
		select {
		case ch <- snap:
		default:
			// Buffer full — drop the oldest and enqueue the newest.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snap:
			default:
			}
		}
	}
}

// sameSnapshot reports whether two snapshots are equal by (GID, Status,
// CompletedLength) — the fields whose change the UI cares about — so the loop
// only fans out on a meaningful diff. DownloadSpeed is intentionally excluded
// from the diff key (it changes every tick, which would defeat the point), but
// a snapshot IS re-sent whenever length/status changes, keeping speed fresh.
func sameSnapshot(a, b []aria2.Download) bool {
	if len(a) != len(b) {
		return false
	}
	ka := diffKeys(a)
	kb := diffKeys(b)
	return reflect.DeepEqual(ka, kb)
}

func diffKeys(dls []aria2.Download) map[string]seen {
	out := make(map[string]seen, len(dls))
	for _, d := range dls {
		out[d.GID] = seen{status: d.Status, completed: d.CompletedLength}
	}
	return out
}

// ErrNoBinary is returned by ExtractBinary when the embedded aria2c binary is
// empty (the build wasn't run through `make aria2c`).
var ErrNoBinary = errors.New("downloader: embedded aria2c binary is empty — run `make aria2c` before building")
