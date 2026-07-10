// Package mediainfo probes real video files for codec/resolution/bitrate —
// used for both sides of a Dedup comparison. Deliberately not conditional on
// whether a file is already tracked: see internal/dedup's doc comment for
// why SAK always reads the real file itself rather than trusting a
// *arr app's own reported quality for one side of the comparison.
package mediainfo

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"
)

// Probe holds the fields needed to build a place.QualityKey, plus the video's
// duration in seconds. Duration is sourced from ffprobe's FORMAT section (not
// the stream-level duration, which MKV/MP4 frequently omit) so it matches the
// value internal/videophash's own duration probe uses for the same file — the
// two must agree because Adult fingerprint give-back stamps this duration and
// rejects a non-positive one. Missing/blank format duration parses to 0.
type Probe struct {
	CodecName string
	Width     int
	Height    int
	BitRate   int64
	Duration  float64
}

// runner executes ffprobe and returns its raw JSON stdout. Injected so
// Prober is testable without a real ffprobe binary or media file.
type runner func(ctx context.Context, path string) ([]byte, error)

type Prober struct {
	run     runner
	timeout time.Duration
}

// New returns a Prober backed by the real ffprobe binary.
func New() *Prober {
	return &Prober{run: runFFprobe, timeout: 30 * time.Second}
}

func runFFprobe(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-select_streams", "v:0",
		"-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe failed: %w", err)
	}
	return out, nil
}

type ffprobeStream struct {
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BitRate   string `json:"bit_rate"`
}

type ffprobeFormat struct {
	Duration string `json:"duration"`
}

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

// Probe runs ffprobe against path (bounded by an internal timeout layered
// onto ctx) and returns the first video stream's codec/resolution/bitrate.
// Returns an error if ffprobe fails or the file has no video stream.
func (p *Prober) Probe(ctx context.Context, path string) (*Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	raw, err := p.run(ctx, path)
	if err != nil {
		return nil, err
	}

	var out ffprobeOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	if len(out.Streams) == 0 {
		return nil, fmt.Errorf("no video stream found in %s", path)
	}

	s := out.Streams[0]
	var bitRate int64
	if s.BitRate != "" {
		// Best-effort: some containers/codecs don't report a stream-level
		// bit_rate at all — 0 is a fine "unknown" fallback, matching
		// place.QualityKey's existing unknown-value convention.
		bitRate, _ = strconv.ParseInt(s.BitRate, 10, 64)
	}

	var duration float64
	if out.Format.Duration != "" {
		// Best-effort like bit_rate: a missing/blank format duration parses to
		// 0, never an error — a file with no reported duration is a valid probe
		// result, just one that can't feed fingerprint give-back.
		duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	}

	return &Probe{
		CodecName: s.CodecName,
		Width:     s.Width,
		Height:    s.Height,
		BitRate:   bitRate,
		Duration:  duration,
	}, nil
}
