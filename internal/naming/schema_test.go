package naming

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestMatchesMovieSchema(t *testing.T) {
	t.Run("conformant Jellyfin folder+file matches", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some Movie (2020) [tmdbid-42].mkv")
		if !MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a conformant Jellyfin folder to match")
		}
	})

	t.Run("conformant Legacy folder+file matches", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020)")
		writeFile(t, folder, "Some Movie (2020).mkv")
		if !MatchesMovieSchema(folder, Legacy) {
			t.Error("expected a conformant Legacy folder to match")
		}
	})

	t.Run("a bare loose file never matches, even with a conformant-looking name", func(t *testing.T) {
		root := t.TempDir()
		path := writeFile(t, root, "Some Movie (2020) [tmdbid-42].mkv")
		if MatchesMovieSchema(path, Jellyfin) {
			t.Error("expected a bare file (no wrapping folder) to never match")
		}
	})

	t.Run("mismatched file name inside a conformant-looking folder does not match", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some.Movie.2020.1080p.BluRay-GROUP.mkv")
		if MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a folder/file name mismatch to not match")
		}
	})

	t.Run("missing tmdbid tag under Jellyfin does not match", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020)")
		writeFile(t, folder, "Some Movie (2020).mkv")
		if MatchesMovieSchema(folder, Jellyfin) {
			t.Error("expected a Legacy-shaped folder to not match under the Jellyfin preset")
		}
	})

	t.Run("Jellyfin-shaped folder does not match under Legacy", func(t *testing.T) {
		root := t.TempDir()
		folder := filepath.Join(root, "Some Movie (2020) [tmdbid-42]")
		writeFile(t, folder, "Some Movie (2020) [tmdbid-42].mkv")
		if MatchesMovieSchema(folder, Legacy) {
			t.Error("expected a Jellyfin-shaped folder to not match under the Legacy preset")
		}
	})

	t.Run("nonexistent path does not match", func(t *testing.T) {
		if MatchesMovieSchema(filepath.Join(t.TempDir(), "nope"), Jellyfin) {
			t.Error("expected a nonexistent path to not match")
		}
	})
}

func TestMatchesSeriesSchema(t *testing.T) {
	t.Run("conformant Jellyfin episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show (2019) [tmdbid-555]", "Season 01"), "Some Show S01E01.mkv")
		if !MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a conformant Jellyfin episode to match")
		}
	})

	t.Run("conformant Legacy episode matches", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show - S01E01.mkv")
		if !MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a conformant Legacy episode to match")
		}
	})

	t.Run("dash-separated file does not match under Jellyfin", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show (2019) [tmdbid-555]", "Season 01"), "Some Show - S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a dash-separated (Legacy-shaped) file to not match under Jellyfin")
		}
	})

	t.Run("space-separated file does not match under Legacy", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a space-separated (Jellyfin-shaped) file to not match under Legacy")
		}
	})

	t.Run("missing tmdbid tag on series folder does not match under Jellyfin", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "Season 01"), "Some Show S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) {
			t.Error("expected a bare-title series folder to not match under Jellyfin")
		}
	})

	t.Run("wrong season folder shape does not match", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, filepath.Join(root, "Some Show", "S01"), "Some Show - S01E01.mkv")
		if MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected an abbreviated season folder name to not match")
		}
	})

	t.Run("a scene-release-named episode does not match either preset", func(t *testing.T) {
		root := t.TempDir()
		videoPath := writeFile(t, root, "Some.Show.S01E01.1080p.WEB-DL.x264-GROUP.mkv")
		if MatchesSeriesSchema(videoPath, Jellyfin) || MatchesSeriesSchema(videoPath, Legacy) {
			t.Error("expected a loose scene-release file to not match either preset")
		}
	})
}
