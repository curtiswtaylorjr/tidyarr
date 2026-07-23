// Package scanschedule is a general Rename/Purge/Dedup scan scheduler — a
// fourth background scheduler joining internal/recheck, internal/adultnewest,
// and internal/api's RunWatchFolders, all of which predate it and all of which
// only ever Scan/propose, never Apply. It is opt-in and 0/off by default for
// every workflow and mode, launched once from cmd/sakms/main.go and cancelled
// via ctx on shutdown, exactly like the three schedulers it mirrors.
//
// # DELIBERATE EXCEPTION: the Scanner interface is a compile-time safety boundary, not an abstraction
//
// This package depends on the Scan workflows through ONE narrow interface,
// Scanner, and NEVER imports internal/dedup, internal/rename, or internal/purge
// directly. That is a deliberate, documented departure from this project's
// "no premature abstraction" convention (CLAUDE.md), in the same register
// internal/recheck's own package doc uses for its "manual by default" exception.
//
// The interface exists for exactly ONE reason and will only ever have ONE
// production implementation (cmd/sakms/scanadapter.go): it makes it
// COMPILE-TIME IMPOSSIBLE for this package's code to reach an Apply-family
// function (dedup.ApplyLibrary, rename.ApplyLibrary, proposals.Repick, a
// fingerprint-submit call, …). ScanLibrary* and ApplyLibrary* are exported,
// same-package sibling functions with no interface seam — a scheduler that
// imported dedup/rename/purge directly COULD call Apply, and no runtime test
// could prove it never does. By never importing those packages, the import
// graph itself forbids it. This is the single most load-bearing invariant in
// the codebase (the staged-for-approval rule: nothing is ever mutated without a
// human having reviewed it at Scan time), so it is worth the one justified
// house-style exception. It is NOT here for polymorphism.
//
// The one place an Apply call could still be wired in is the single adapter
// implementation, which legitimately needs proposals.ReplacePending to persist
// its Scan results. That residual surface is guarded by an independent static
// allowlist test (see allowlist_test.go) that scans this package's source AND
// the adapter's source and fails on any reference to a mutating function from
// dedup/rename/purge/proposals other than Scan*-family functions and
// ReplacePending.
//
// # Boundary note: the allowlist covers dedup/rename/purge/proposals, not internal/api
//
// The adapter also calls back into internal/api (scanschedule_support.go's
// EagerComputeAndCacheVMAF and the naming/threshold/root-folder resolve
// helpers) — a seam the allowlist test does not scan, since api is not one of
// the four restricted packages the invariant is about. Every call across that
// seam today is resolve-or-cache-only (never Apply-shaped), so this is not a
// live gap, but it is the boundary of what the static check actually proves:
// a future change wiring a mutating api-package call into the adapter would
// not trip it. If that boundary ever needs the same guarantee, either extend
// the allowlist's restricted-package set to include api's mutating surface, or
// keep api's adapter-facing helpers deliberately read-only/cache-only by
// convention and say so at their call sites, as scanschedule_support.go
// already does.
package scanschedule

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/labbersanon/sakms/internal/dedupscan"
	"github.com/labbersanon/sakms/internal/mode"
	"github.com/labbersanon/sakms/internal/settings"
)

// Scanner is the narrow, Scan-only contract this scheduler drives. Every method
// only ever triggers a workflow's propose phase (ScanLibrary* + persist) and
// returns an error — there is deliberately no Apply/Dismiss/Repick shaped
// method, and there never will be. See the package doc for why this interface
// exists (a compile-time safety boundary), and cmd/sakms/scanadapter.go for its
// single production implementation.
//
// ScanDedup additionally takes eagerVMAF: when true, the adapter eagerly
// computes VMAF scores for the groups the scan found (star topology, plan AC6),
// so the on-demand view path serves a warm cache. The flag is read from
// DedupVMAFEnabledKey by the cycle (below), not by the adapter, so the
// scheduler stays in control of the eager behavior.
type Scanner interface {
	ScanRename(ctx context.Context, m mode.Mode) error
	ScanPurge(ctx context.Context, m mode.Mode) error
	ScanDedup(ctx context.Context, m mode.Mode, eagerVMAF bool) error
}

// Per-workflow interval settings keys (whole seconds; 0/unset/blank/negative
// all mean "off", the opt-in gate and the default). One interval per workflow
// covers all three modes for that workflow. internal/api mirrors these strings
// in its own GET/PUT settings handlers rather than importing this package, the
// same import-avoidance recheck/adultnewest use to stay independently
// deletable.
const (
	RenameIntervalKey = "rename_scan_interval_seconds"
	PurgeIntervalKey  = "purge_scan_interval_seconds"
	DedupIntervalKey  = "dedup_scan_interval_seconds"

	// DedupVMAFEnabledKey gates the eager-VMAF fan-out on a scheduled Dedup
	// cycle (plan AC6). Off by default; independent of DedupIntervalKey (a
	// Dedup schedule can run with or without eager VMAF).
	DedupVMAFEnabledKey = "dedup_vmaf_scan_enabled"
)

// workflow identifies which of the three Scan workflows a loop/cycle drives.
type workflow string

const (
	workflowRename workflow = "rename"
	workflowPurge  workflow = "purge"
	workflowDedup  workflow = "dedup"
)

// allModes is the fixed set every workflow cycle scans, in order.
var allModes = []mode.Mode{mode.Movies, mode.Series, mode.Adult}

// LoadInterval reads key and returns it as a Duration, or 0 ("off") for any
// unset, blank, non-integer, or non-positive value — a tolerant read (a corrupt
// or missing value degrades to "off"/manual-first rather than erroring the boot
// path), identical in shape to recheck.LoadInterval.
func LoadInterval(ctx context.Context, settingsStore *settings.Store, key string) time.Duration {
	v, err := settingsStore.Get(ctx, key)
	if err != nil {
		return 0 // unset (ErrNotFound) or a real store error → treat as off
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// Run launches the three per-workflow scheduler loops (Rename, Purge, Dedup)
// and returns immediately — each loop is its own goroutine, cancelled via ctx
// on shutdown. A workflow whose interval is <= 0 at boot (the default, since
// every key is unset until an operator opts in) starts NOTHING for that
// workflow; re-enabling a workflow that was off at boot needs a restart, same
// as recheck. Safe to call unconditionally.
//
// ctx is the caller's shutdown-aware context, threaded straight through to
// every Scan call (unlike dedupScanHandler, which needs the Hub's base context
// because its work outlives a request — here the scheduler IS long-lived, so
// ctx is already the right lifetime).
func Run(ctx context.Context, scanner Scanner, settingsStore *settings.Store, hub *dedupscan.Hub) {
	for _, wk := range []struct {
		wf  workflow
		key string
	}{
		{workflowRename, RenameIntervalKey},
		{workflowPurge, PurgeIntervalKey},
		{workflowDedup, DedupIntervalKey},
	} {
		key := wk.key // capture per iteration for the reload closure
		interval := LoadInterval(ctx, settingsStore, key)
		reload := func() time.Duration { return LoadInterval(ctx, settingsStore, key) }
		go runLoop(ctx, wk.wf, reload, interval, scanner, settingsStore, hub)
	}
}

// runLoop drives one workflow's scheduler loop until ctx is cancelled. interval
// is the boot-time cadence; if it is <= 0 the loop returns immediately and
// starts nothing (the opt-in gate). When enabled, each tick calls reload to
// re-read the interval so an operator can retune or disable it live (a change to
// 0 stops the loop cleanly). Mirrors recheck.Run's select-loop exactly, plus the
// AC15 skip-not-queue drain below. reload is injected (rather than reading
// settings directly) so the AC15 timing test can drive a fixed sub-second
// cadence deterministically; production passes a LoadInterval closure.
func runLoop(ctx context.Context, wf workflow, reload func() time.Duration, interval time.Duration, scanner Scanner, settingsStore *settings.Store, hub *dedupscan.Hub) {
	if interval <= 0 {
		return // opt-in gate: off by default, honoring "manual first"
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("scanschedule: %s scan scheduler enabled (every %s) — a deliberate opt-in exception to manual-by-default; Scan-only, never Apply", wf, interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := reload()
			if cur <= 0 {
				log.Printf("scanschedule: %s interval set to 0 — stopping (restart to re-enable)", wf)
				return
			}
			if cur != interval {
				interval = cur
				ticker.Reset(cur)
			}

			runCycle(ctx, wf, scanner, settingsStore, hub)

			// AC15: a runCycle that outran the interval leaves at most one
			// buffered tick (time.Ticker drops the rest). Drain that one tick so
			// the next cycle waits a fresh full interval instead of re-firing
			// back-to-back — skip, don't queue, an overrun.
			select {
			case <-ticker.C:
			default:
			}
		}
	}
}

// runCycle performs exactly one scan pass for wf across all three modes and
// returns — the single-tick logic, extracted from runLoop so tests exercise it
// directly (against a fake Scanner) rather than sleeping on a ticker. For Dedup
// it reads the eager-VMAF flag once and applies the Hub concurrency guard per
// mode (runDedupCycle); Rename/Purge have no guard to reuse (none exists) and
// run last-wins, as documented in the plan (safe: Scan mutates no files and
// ReplacePending is transactional). A single mode's failure or panic is
// isolated and never aborts the other modes.
func runCycle(ctx context.Context, wf workflow, scanner Scanner, settingsStore *settings.Store, hub *dedupscan.Hub) {
	var eagerVMAF bool
	if wf == workflowDedup {
		// GetBool degrades to the default on any error, so a store hiccup means
		// "no eager VMAF this cycle", never a failed cycle.
		eagerVMAF, _ = settingsStore.GetBool(ctx, DedupVMAFEnabledKey, false)
	}

	for _, m := range allModes {
		if ctx.Err() != nil {
			return
		}
		switch wf {
		case workflowRename:
			runScan(wf, m, func() error { return scanner.ScanRename(ctx, m) })
		case workflowPurge:
			runScan(wf, m, func() error { return scanner.ScanPurge(ctx, m) })
		case workflowDedup:
			runDedupCycle(ctx, m, eagerVMAF, scanner, hub)
		}
	}
}

// runScan runs one Rename/Purge scan for a single mode with fault isolation: an
// error is logged and dropped, and a panic is recovered so one bad mode never
// crashes the long-lived scheduler goroutine or aborts the remaining modes.
func runScan(wf workflow, m mode.Mode, fn func() error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("scanschedule: %s scan for %s panicked: %v", wf, m, rec)
		}
	}()
	if err := fn(); err != nil {
		log.Printf("scanschedule: %s scan for %s: %v", wf, m, err)
	}
}

// runDedupCycle runs one Dedup scan for a single mode, guarded by the existing
// dedupscan.Hub — identical mutual exclusion to the manual trigger (plan AC14),
// including the same operator-visible "Scanning..." SSE/in-flight state. If a
// scan for this mode is already running (manual or scheduled), the cycle is
// skipped rather than racing it.
//
// Finish is deferred FIRST so it runs LAST (after the recover): together they
// guarantee the in-flight flag always clears even on a mid-scan panic — a wedge
// here would make every future manual and scheduled Dedup scan of this mode
// return 409 forever. Terminal SSE choreography (a "done"/"error" frame) is
// intentionally not published: the frontend's existing scan-status liveness
// backstop (dedupScanStatusHandler) reconciles a connected client once Finish
// clears in-flight, and the plan only promises the "Scanning..." flip here.
//
// Deliberate scope of the guard: when eager VMAF is enabled it runs INSIDE
// ScanDedup (the eager fan-out needs the in-memory groups the scan just found,
// which the error-only Scanner interface does not surface back to this
// function), so the guard necessarily spans the eager VMAF compute too. That is
// intended, not incidental: the mode is genuinely still busy computing scores
// for the groups it just found, so keeping it in-flight (surfacing "Scanning..."
// and 409-ing a concurrent manual scan) for the eager duration is correct — it
// prevents a second scan starting on top of an unfinished cycle. Eager VMAF
// mutates only the vmaf_scores cache, never proposals, so this is a UX/serialize
// choice, never a safety one.
func runDedupCycle(ctx context.Context, m mode.Mode, eagerVMAF bool, scanner Scanner, hub *dedupscan.Hub) {
	if !hub.TryStart(string(m)) {
		log.Printf("scanschedule: scheduled dedup for %s skipped — a scan is already running", m)
		return
	}
	defer hub.Finish(string(m))
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("scanschedule: dedup scan for %s panicked: %v", m, rec)
		}
	}()

	if err := scanner.ScanDedup(ctx, m, eagerVMAF); err != nil {
		log.Printf("scanschedule: dedup scan for %s: %v", m, err)
	}
}
