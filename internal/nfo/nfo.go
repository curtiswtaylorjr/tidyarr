// Package nfo reads Kodi/Jellyfin-format .nfo sidecar files alongside video
// files and extracts metadata — primarily the TMDB ID, which lets ScanLibrary
// skip the fuzzy filename search when an authoritative ID is already on disk.
//
// Both common XML shapes are handled:
//   <tmdbid>603</tmdbid>                         (Kodi flat field)
//   <uniqueid type="tmdb">603</uniqueid>          (Jellyfin / newer Kodi)
//
// Two sidecar path conventions are tried, in preference order:
//  1. Same-basename sidecar: /path/to/Movie.mkv  → /path/to/Movie.nfo
//  2. Folder-level sidecar:  /path/to/Movie.mkv  → /path/to/movie.nfo
//
// Errors from open/parse are silently swallowed by ReadSidecar — a missing or
// malformed .nfo is treated as "no hint available," never a fatal scan error.
package nfo

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MovieNFO holds the fields SAK reads from a movie .nfo sidecar.
// Zero values indicate the field was absent or unparseable.
type MovieNFO struct {
	TMDBID int
	Title  string
	Year   int
}

// xmlMovie is the raw XML shape — handles both the flat <tmdbid> field and the
// <uniqueid type="tmdb"> variant used by Jellyfin and newer Kodi builds.
type xmlMovie struct {
	XMLName   xml.Name `xml:"movie"`
	Title     string   `xml:"title"`
	Year      int      `xml:"year"`
	TMDBIDTag int      `xml:"tmdbid"`
	UniqueIDs []xmlUID `xml:"uniqueid"`
}

// xmlUID uses a string Value so that non-numeric IDs like IMDB's "tt0133093"
// don't cause xml.Decode to fail — only the tmdb type is converted to int.
type xmlUID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

// SidecarPaths returns the candidate .nfo paths for an entry, in preference
// order. The caller should try each in turn and stop at the first that exists.
//
// Movies are often stored as a folder ("Movie Title (2001)/movie.mkv").
// ScanRootFolder returns the folder as the atomic entry, so entryPath may be a
// directory. In that case the sidecar is inside: "{dir}/{dirname}.nfo" (Kodi
// default) or "{dir}/movie.nfo" (folder-level alternative). When entryPath is
// a plain file, the same-basename sidecar and the parent-folder movie.nfo are
// tried.
func SidecarPaths(entryPath string) []string {
	if info, err := os.Stat(entryPath); err == nil && info.IsDir() {
		name := filepath.Base(entryPath)
		return []string{
			filepath.Join(entryPath, name+".nfo"),
			filepath.Join(entryPath, "movie.nfo"),
		}
	}
	ext := filepath.Ext(entryPath)
	baseSidecar := strings.TrimSuffix(entryPath, ext) + ".nfo"
	folderSidecar := filepath.Join(filepath.Dir(entryPath), "movie.nfo")
	if baseSidecar == folderSidecar {
		return []string{baseSidecar}
	}
	return []string{baseSidecar, folderSidecar}
}

// Read parses a .nfo file at the given path and returns the extracted fields.
func Read(path string) (MovieNFO, error) {
	f, err := os.Open(path)
	if err != nil {
		return MovieNFO{}, err
	}
	defer f.Close()

	var raw xmlMovie
	if err := xml.NewDecoder(f).Decode(&raw); err != nil {
		return MovieNFO{}, err
	}

	m := MovieNFO{Title: raw.Title, Year: raw.Year}

	// Flat <tmdbid> field takes precedence; fall back to <uniqueid type="tmdb">.
	if raw.TMDBIDTag != 0 {
		m.TMDBID = raw.TMDBIDTag
	} else {
		for _, uid := range raw.UniqueIDs {
			if uid.Type == "tmdb" {
				if id, err := strconv.Atoi(strings.TrimSpace(uid.Value)); err == nil && id != 0 {
					m.TMDBID = id
					break
				}
			}
		}
	}

	return m, nil
}

// ReadSidecar tries each candidate .nfo path for videoPath and returns the
// first successfully parsed result. Returns a zero MovieNFO (and no error) if
// no readable sidecar exists — never returns an error from missing/bad files.
func ReadSidecar(videoPath string) MovieNFO {
	for _, p := range SidecarPaths(videoPath) {
		if m, err := Read(p); err == nil {
			return m
		}
	}
	return MovieNFO{}
}
