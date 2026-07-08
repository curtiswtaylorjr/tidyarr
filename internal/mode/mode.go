// Package mode builds the live client(s) for one of Tidyarr's three
// isolated modes — Movies, Series, or Adult — from whatever connection is
// currently configured in Settings. A Session is cheap to build (an HTTP
// client wrapper, nothing cached), so it's constructed fresh per request:
// a connection edited in Settings takes effect on the very next Scan/Apply,
// no restart required.
package mode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/curtiswtaylorjr/tidyarr/internal/bravesearch"
	"github.com/curtiswtaylorjr/tidyarr/internal/connections"
	"github.com/curtiswtaylorjr/tidyarr/internal/identify"
	"github.com/curtiswtaylorjr/tidyarr/internal/ollama"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
	"github.com/curtiswtaylorjr/tidyarr/internal/settings"
	"github.com/curtiswtaylorjr/tidyarr/internal/stashbox"
	"github.com/curtiswtaylorjr/tidyarr/internal/throttle"
	"github.com/curtiswtaylorjr/tidyarr/internal/tpdbrest"
)

// Mode is one of Tidyarr's three isolated library contexts. Never blended —
// see the design spec's "Mode replaces checkboxes" section for why.
type Mode string

const (
	Movies Mode = "movies"
	Series Mode = "series"
	Adult  Mode = "adult"
)

// AdultOllamaModelKey is the settings key holding the Ollama model name the
// Adult identification pipeline runs against. Stored in settings (not a
// connections column) because it's a non-secret scalar with no schema of its
// own. Empty/unset means "identification not configured": Build leaves
// sess.Identify nil rather than guessing a model. Exported so internal/api can
// read/write the same key without duplicating the string literal.
const AdultOllamaModelKey = "adult_ollama_model"

// adultThrottleInterval is the per-host minimum call spacing for the Adult
// identification pipeline's external services — technical call-spacing
// (politeness to StashDB/FansDB/TPDB/Brave), not a user-facing setting, so a
// constant is correct.
const adultThrottleInterval = 1 * time.Second

// service reports which connections.Store key and servarr.App back this
// mode's primary client.
func (m Mode) service() (service string, app servarr.App, err error) {
	switch m {
	case Movies:
		return "radarr", servarr.Radarr, nil
	case Series:
		return "sonarr", servarr.Sonarr, nil
	case Adult:
		// Adult's primary client is Whisparr V3 (a Radarr fork — see
		// internal/servarr), hard-required for every Adult workflow. The
		// identification pipeline (StashDB/FansDB/TPDB/Ollama, internal/identify)
		// is built separately and tolerantly — see buildIdentifier.
		return "whisparr", servarr.Whisparr, nil
	default:
		return "", 0, fmt.Errorf("mode %q: unknown mode", m)
	}
}

// Session holds the live client(s) for one mode.
type Session struct {
	Mode    Mode
	Servarr *servarr.Client

	// Identify is the AI-assisted content-identification pipeline, populated
	// ONLY for Adult mode and ONLY when its backbone (an Ollama connection AND
	// the adult_ollama_model setting) is configured; nil otherwise — including
	// for every Movies/Series session. Consumers must nil-check before use.
	Identify *identify.Identifier
}

// Build constructs a Session for m using the connection currently configured
// in store. Returns an error if m isn't supported yet, or if its service has
// no connection configured (Settings hasn't been filled in for it yet).
func Build(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client, m Mode) (*Session, error) {
	service, app, err := m.service()
	if err != nil {
		return nil, err
	}
	conn, err := store.Get(ctx, service)
	if err != nil {
		if errors.Is(err, connections.ErrNotFound) {
			return nil, fmt.Errorf("mode %q: %s isn't configured yet — add it in Settings first", m, service)
		}
		return nil, fmt.Errorf("mode %q: loading %s connection: %w", m, service, err)
	}
	client := servarr.New(servarr.Config{BaseURL: conn.URL, APIKey: conn.APIKey, App: app}, httpClient)

	sess := &Session{Mode: m, Servarr: client}
	if m == Adult {
		id, err := buildIdentifier(ctx, store, settingsStore, httpClient)
		if err != nil {
			return nil, fmt.Errorf("mode %q: building identifier: %w", m, err)
		}
		sess.Identify = id
	}
	return sess, nil
}

// buildIdentifier assembles the Adult identification pipeline from whatever is
// configured. Tolerant by design: the Ollama connection AND the
// adult_ollama_model setting are the backbone — without either, there is no
// identifier at all (returns nil, nil), because ParseFilename would nil-panic
// on a missing Ollama client. Every other client (stashdb/fansdb/tpdb/brave)
// is optional: a missing connection yields a nil client, which BoxSearcher and
// Identify already treat as "not configured" rather than erroring. A real
// store error (anything other than "not configured") propagates.
func buildIdentifier(ctx context.Context, store *connections.Store, settingsStore *settings.Store, httpClient *http.Client) (*identify.Identifier, error) {
	ollamaConn, err := optionalConn(ctx, store, "ollama")
	if err != nil {
		return nil, err
	}
	if ollamaConn == nil {
		return nil, nil // no Ollama backbone → identification not configured
	}
	model, err := settingsStore.Get(ctx, AdultOllamaModelKey)
	if errors.Is(err, settings.ErrNotFound) {
		return nil, nil // no model → do NOT guess one
	}
	if err != nil {
		return nil, err
	}
	if model == "" {
		return nil, nil // stored but blank → same as unconfigured
	}

	boxes := map[string]*stashbox.Client{}
	for _, name := range []string{"stashdb", "fansdb"} {
		conn, err := optionalConn(ctx, store, name)
		if err != nil {
			return nil, err
		}
		if conn != nil {
			boxes[name] = stashbox.New(stashbox.Config{
				Endpoint: conn.URL, APIKey: conn.APIKey, IsBearer: false, HasVoteField: true,
			}, httpClient)
		}
	}

	var tpdb *tpdbrest.Client
	if conn, err := optionalConn(ctx, store, "tpdb"); err != nil {
		return nil, err
	} else if conn != nil {
		tpdb = tpdbrest.New(conn.URL, conn.APIKey, httpClient)
	}

	var brave *bravesearch.Client
	if conn, err := optionalConn(ctx, store, "brave"); err != nil {
		return nil, err
	} else if conn != nil {
		brave = bravesearch.New(conn.URL, conn.APIKey, httpClient)
	}

	return &identify.Identifier{
		Boxes:    identify.NewBoxSearcher(boxes, tpdb),
		Ollama:   ollama.New(ollamaConn.URL, model, httpClient),
		Brave:    brave,
		Throttle: throttle.New(adultThrottleInterval),
	}, nil
}

// optionalConn returns the connection for service, or (nil, nil) if it simply
// isn't configured — collapsing connections.ErrNotFound into "absent" so
// callers can treat optional services uniformly. Any other error propagates.
func optionalConn(ctx context.Context, store *connections.Store, service string) (*connections.Connection, error) {
	conn, err := store.Get(ctx, service)
	if errors.Is(err, connections.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conn, nil
}
