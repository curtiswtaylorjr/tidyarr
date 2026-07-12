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

func TestAdultFileName(t *testing.T) {
	cases := []struct {
		name                       string
		studio, title, date, phash string
		ext                        string
		want                       string
	}{
		{"all fields", "Brazzers", "Scene Title", "2021-03-04", "abc123", ".mp4",
			"Brazzers - Scene Title (2021-03-04) [phash-abc123].mp4"},
		{"missing studio drops the prefix", "", "Scene Title", "2021-03-04", "abc123", ".mkv",
			"Scene Title (2021-03-04) [phash-abc123].mkv"},
		{"missing date drops the segment", "Brazzers", "Scene Title", "", "abc123", ".mp4",
			"Brazzers - Scene Title [phash-abc123].mp4"},
		{"missing phash drops the tag", "Brazzers", "Scene Title", "2021-03-04", "", ".mp4",
			"Brazzers - Scene Title (2021-03-04).mp4"},
		{"title only", "", "Scene Title", "", "", ".avi", "Scene Title.avi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AdultFileName(c.studio, c.title, c.date, c.phash, c.ext); got != c.want {
				t.Errorf("AdultFileName(%q, %q, %q, %q, %q) = %q, want %q", c.studio, c.title, c.date, c.phash, c.ext, got, c.want)
			}
		})
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
