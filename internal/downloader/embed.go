package downloader

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// aria2cBinary is the static aria2c executable SAK runs as its download
// subprocess, embedded directly into the Go binary so there's nothing extra
// to install alongside it — the same "one self-contained binary" shape as
// internal/web's embedded frontend bundle.
//
// The embedded file at assets/aria2c is NOT committed to git (it's a
// multi-MB platform artifact — see .gitignore), so this //go:embed fails
// cleanly on a clean checkout with `pattern assets/aria2c: no matching files
// found` until `make aria2c` (cmd/download-aria2c) fetches it, exactly like
// internal/web/static's frontend-bundle embed. The Dockerfile runs that
// fetch in a build stage before `go build`.
//
//go:embed assets/aria2c
var aria2cBinary []byte

// ExtractBinary writes the embedded aria2c to a fresh temp directory,
// chmods it 0755, and returns its path — where the Manager runs it from.
// The caller owns the temp dir and should clean it up at shutdown (or accept
// the OS reaping /tmp). Returns ErrNoBinary if the embed is empty (the build
// skipped `make aria2c`).
func ExtractBinary() (path string, err error) {
	if len(aria2cBinary) == 0 {
		return "", ErrNoBinary
	}
	dir, err := os.MkdirTemp("", "sakms-aria2c-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir for aria2c: %w", err)
	}
	path = filepath.Join(dir, "aria2c")
	if err := os.WriteFile(path, aria2cBinary, 0o755); err != nil {
		return "", fmt.Errorf("writing aria2c binary: %w", err)
	}
	return path, nil
}
