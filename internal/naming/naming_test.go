package naming

import "testing"

func TestValid(t *testing.T) {
	if !Valid(Jellyfin) || !Valid(Legacy) {
		t.Error("expected both Jellyfin and Legacy to be valid presets")
	}
	if Valid(Preset("bogus")) {
		t.Error("expected an unrecognized preset to be invalid")
	}
}

func TestMovieFolderName(t *testing.T) {
	cases := []struct {
		preset       Preset
		title        string
		year, tmdbID int
		want         string
	}{
		{Jellyfin, "Some Movie", 2020, 42, "Some Movie (2020) [tmdbid-42]"},
		{Legacy, "Some Movie", 2020, 42, "Some Movie (2020)"},
		{Jellyfin, "Some Movie", 0, 42, "Some Movie [tmdbid-42]"},
		{Jellyfin, "Some Movie", 2020, 0, "Some Movie (2020)"},
		{Jellyfin, "Some Movie", 0, 0, "Some Movie"},
	}
	for _, c := range cases {
		if got := MovieFolderName(c.preset, c.title, c.year, c.tmdbID); got != c.want {
			t.Errorf("MovieFolderName(%v, %q, %d, %d) = %q, want %q", c.preset, c.title, c.year, c.tmdbID, got, c.want)
		}
	}
}

func TestMovieFileName(t *testing.T) {
	if got := MovieFileName(Jellyfin, "Some Movie", 2020, 42, ".mkv"); got != "Some Movie (2020) [tmdbid-42].mkv" {
		t.Errorf("unexpected file name: %q", got)
	}
	if got := MovieFileName(Legacy, "Some Movie", 2020, 42, ".mkv"); got != "Some Movie (2020).mkv" {
		t.Errorf("unexpected legacy file name: %q", got)
	}
}

func TestSeriesFolderName(t *testing.T) {
	if got := SeriesFolderName(Jellyfin, "Some Show", 2019, 555); got != "Some Show (2019) [tmdbid-555]" {
		t.Errorf("unexpected jellyfin series folder: %q", got)
	}
	if got := SeriesFolderName(Legacy, "Some Show", 2019, 555); got != "Some Show" {
		t.Errorf("expected legacy series folder to stay a bare title, got %q", got)
	}
}

func TestSeasonDirName(t *testing.T) {
	if got := SeasonDirName(3); got != "Season 03" {
		t.Errorf("expected %q, got %q", "Season 03", got)
	}
}

func TestEpisodeFileName(t *testing.T) {
	if got := EpisodeFileName(Jellyfin, "Show Name", 3, 5, "Episode Title", ".mkv"); got != "Show Name S03E05 Episode Title.mkv" {
		t.Errorf("unexpected jellyfin file name: %q", got)
	}
	if got := EpisodeFileName(Jellyfin, "Show Name", 3, 5, "", ".mkv"); got != "Show Name S03E05.mkv" {
		t.Errorf("unexpected jellyfin file name with no episode title: %q", got)
	}
	if got := EpisodeFileName(Legacy, "Show Name", 3, 5, "Episode Title", ".mkv"); got != "Show Name - S03E05 - Episode Title.mkv" {
		t.Errorf("unexpected legacy file name: %q", got)
	}
	if got := EpisodeFileName(Legacy, "Show Name", 3, 5, "", ".mkv"); got != "Show Name - S03E05.mkv" {
		t.Errorf("unexpected legacy file name with no episode title: %q", got)
	}
}
