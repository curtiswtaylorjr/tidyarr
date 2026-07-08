// Package config loads Tidyarr's runtime configuration from the environment.
package config

import (
	"cmp"
	"os"
)

// Config holds settings resolved once at startup.
type Config struct {
	// Addr is the HTTP listen address, e.g. ":8080".
	Addr string
	// DataDir holds tidyarr.db and anything else Tidyarr owns on disk.
	DataDir string
}

// FromEnv reads Config from the environment, applying defaults for anything unset.
func FromEnv() Config {
	return Config{
		Addr:    cmp.Or(os.Getenv("TIDYARR_ADDR"), ":8080"),
		DataDir: cmp.Or(os.Getenv("TIDYARR_DATA_DIR"), "./data"),
	}
}
