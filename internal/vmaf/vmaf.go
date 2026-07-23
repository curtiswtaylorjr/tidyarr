// Package vmaf computes VMAF (Video Multi-Method Assessment Fusion)
// perceptual-quality scores by shelling out to ffmpeg's libvmaf filter, one
// candidate file measured against one reference (a Dedup group's primary).
//
// Execution locus (vmaf-backend plan, "VMAF execution locus" decision, Option
// A): the ffmpeg subprocess runs IN-PROCESS in the sakms server, the same
// exec.CommandContext pattern internal/phash uses — it is deliberately NOT
// dispatched through internal/nodes' dispatcher, so it is NOT covered by the
// node CPU governor. This package's only resource bound is the single
// package-level concurrency semaphore below plus a per-computation timeout;
// that limit is honest, not borrowed from a governor that does not apply here.
package vmaf

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxConcurrentVMAF bounds how many VMAF computations may run in-process at
// once, across EVERY caller — both the on-demand HTTP view path (Stage 2) and
// the eager scheduled fan-out (Stage 3) call Compute, and Compute is the sole
// acquirer of the single vmafSem below, so combined concurrency across both
// paths can never exceed this cap (plan AC7). It is intentionally low: a VMAF
// computation is a full-decode of two files and is CPU-heavy.
const maxConcurrentVMAF = 2

// computeTimeout bounds a single candidate-vs-reference computation. A pair
// that cannot finish within it is failed (context-cancelled ffmpeg), so one
// pathological file never wedges a slot forever.
const computeTimeout = 15 * time.Minute

// vmafSem is the ONE shared concurrency limiter for the whole package. It is a
// package-level singleton on purpose (plan AC7 / Critic round 1): making it
// per-call or per-path would let each path stay within its own bound while
// their combined load silently exceeded the cap. Every caller funnels through
// Compute, which is the only code that acquires it.
var vmafSem = make(chan struct{}, maxConcurrentVMAF)

// computeFunc runs the real ffmpeg+libvmaf computation for one pair. It is a
// package var rather than a direct call ONLY so tests can substitute a
// deterministic stub (plan AC7's required observation seam): the semaphore is
// acquired inside Compute BEFORE computeFunc runs, so a test that injects a
// blocking stub recording max-observed concurrency and launches many
// concurrent Compute calls can assert the semaphore bound holds without
// depending on real ffmpeg subprocess timing. Production always uses
// runFFmpegVMAF.
var computeFunc = runFFmpegVMAF

// Compute returns the VMAF score of candidatePath measured against
// referencePath, a value in roughly [0, 100] where higher is closer to the
// reference. It acquires the shared package semaphore (blocking, or returning
// ctx.Err() if ctx is cancelled while waiting) and applies its own
// computeTimeout on top of the caller's ctx, so callers need not add either.
func Compute(ctx context.Context, candidatePath, referencePath string) (float64, error) {
	select {
	case vmafSem <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-vmafSem }()

	cctx, cancel := context.WithTimeout(ctx, computeTimeout)
	defer cancel()
	return computeFunc(cctx, candidatePath, referencePath)
}

// vmafScoreRE matches libvmaf's aggregate (pooled-mean) score line, which the
// filter prints once to stderr, e.g. "[Parsed_libvmaf_0 @ 0x..] VMAF score:
// 98.597452". This single line is the pooled mean across all frames — the
// value callers want — so scraping it is sufficient; the per-frame JSON log is
// not needed.
var vmafScoreRE = regexp.MustCompile(`VMAF score:\s*([0-9]+(?:\.[0-9]+)?)`)

// runFFmpegVMAF shells out to ffmpeg's libvmaf filter for one pair and parses
// the pooled-mean score off stderr.
//
// The scale2ref stage is mandatory, not cosmetic: Dedup candidates are the
// same content at DIFFERENT qualities, so a resolution mismatch is the common
// case, and libvmaf errors outright ("Error reinitializing filters!") when its
// two inputs differ in size. scale2ref scales the candidate (input 0) up/down
// to the reference's (input 1) dimensions first, then libvmaf compares them.
//
// Known limitation (documented, not silently handled): VMAF pairs frames 1:1,
// so a frame-rate / frame-count mismatch between candidate and reference skews
// the score. The common Dedup case is a matching frame rate; normalising fps
// is left as a deliberate follow-up rather than guessed at here.
func runFFmpegVMAF(ctx context.Context, candidatePath, referencePath string) (float64, error) {
	const filter = "[0:v][1:v]scale2ref=flags=bicubic[dist][ref];[dist][ref]libvmaf"
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin",
		"-i", candidatePath,
		"-i", referencePath,
		"-lavfi", filter,
		"-f", "null",
		"-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("vmaf: computing %s vs %s: %w\n%s",
			candidatePath, referencePath, err, tailLines(stderr.String(), 8))
	}
	m := vmafScoreRE.FindStringSubmatch(stderr.String())
	if m == nil {
		return 0, fmt.Errorf("vmaf: no VMAF score in ffmpeg output for %s vs %s\n%s",
			candidatePath, referencePath, tailLines(stderr.String(), 8))
	}
	score, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("vmaf: parsing score %q for %s vs %s: %w",
			m[1], candidatePath, referencePath, err)
	}
	return score, nil
}

// Available reports whether this ffmpeg build actually has the libvmaf filter
// compiled in — distinct from ffmpeg merely being on PATH. The deployment
// image's ffmpeg package is a separately-resolved concern (some Debian /
// jellyfin-ffmpeg builds ship without libvmaf), so callers and tests use this
// to fail/skip cleanly with a clear message instead of an opaque filter error.
func Available(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-filters").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "libvmaf")
}

// tailLines returns at most the last n non-empty lines of s, so an error wraps
// ffmpeg's actual complaint without dumping its full multi-hundred-line log.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		out = append([]string{lines[i]}, out...)
	}
	return strings.Join(out, "\n")
}
