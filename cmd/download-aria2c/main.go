// Command download-aria2c fetches the correct static aria2c binary for the
// build platform and writes it to internal/downloader/assets/aria2c, where
// internal/downloader/embed.go's //go:embed picks it up at build time.
//
// Run it via `make aria2c` (or `go run ./cmd/download-aria2c`) BEFORE
// `go build ./...` — the embed fails cleanly with a build error until the
// binary exists, exactly like internal/web/static's frontend-bundle embed.
//
// The binary itself is deliberately NOT committed (see .gitignore): it's a
// multi-MB platform artifact, mirroring the static/ frontend-bundle
// precedent — generated locally or in the Docker build, never in git.
//
// Source: abcfy2/aria2-static-build, a maintained static-build fork (the
// upstream aria2/aria2 release ships no linux static binary — only android/
// windows/source). We fetch the musl-static x86_64 build so the extracted
// binary has no shared-library dependencies inside the headless container.
// Only linux/amd64 is fetched for now (the sole production target); a second
// platform is an additive case in platformAsset when one is needed.
package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// aria2Version pins the static-build release fetched. Bump deliberately —
// a new aria2 release is a reviewed change, not an automatic latest-follow.
const aria2Version = "1.37.0"

// outPath is where the fetched binary lands, relative to the repo root
// resolved from this source file's own location (so `go run` works from
// anywhere in the tree, same trick cmd/gendto uses).
const outRelPath = "internal/downloader/assets/aria2c"

// platformAsset returns the static-build asset name and the path of the
// aria2c binary inside its zip for the current GOOS/GOARCH. Only linux/amd64
// is supported today; anything else is a hard error rather than a silent
// wrong-arch fetch.
func platformAsset() (assetName, binInZip string, err error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "aria2-x86_64-linux-musl_static.zip", "aria2c", nil
	default:
		return "", "", fmt.Errorf("no static aria2c build wired for %s/%s (only linux/amd64 today — add a case in platformAsset)", runtime.GOOS, runtime.GOARCH)
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("download-aria2c: %v", err)
	}
}

func run() error {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("could not resolve source file path")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	outPath := filepath.Join(repoRoot, filepath.FromSlash(outRelPath))

	assetName, binInZip, err := platformAsset()
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://github.com/abcfy2/aria2-static-build/releases/download/%s/%s", aria2Version, assetName)

	log.Printf("fetching %s", url)
	zipBytes, err := fetch(url)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", url, err)
	}

	binBytes, err := extractFromZip(zipBytes, binInZip)
	if err != nil {
		return fmt.Errorf("extracting %q from archive: %w", binInZip, err)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("creating assets dir: %w", err)
	}
	if err := os.WriteFile(outPath, binBytes, 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	log.Printf("wrote %s (%d bytes)", outPath, len(binBytes))
	return nil
}

// fetch downloads url, following redirects, and returns the whole body.
func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// extractFromZip returns the bytes of the entry whose base name is binName.
func extractFromZip(zipBytes []byte, binName string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("archive has no entry named %q", binName)
}
