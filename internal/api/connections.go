// Package api implements Tidyarr's HTTP API.
package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/curtiswtaylorjr/tidyarr/internal/ollama"
	"github.com/curtiswtaylorjr/tidyarr/internal/servarr"
	"github.com/curtiswtaylorjr/tidyarr/internal/stashapi"
)

// ConnectionTestRequest is enough to construct a client and make one real,
// read-only call against it — the same thing Settings' "Test connection"
// button does. Nothing here is persisted.
type ConnectionTestRequest struct {
	Service string `json:"service"` // "radarr" | "sonarr" | "ollama" | "stash"
	URL     string `json:"url"`
	APIKey  string `json:"apiKey,omitempty"`
}

// ConnectionTestResult reports whether the test call succeeded. A false OK
// with a populated Error is the normal, expected shape for "wrong URL" or
// "wrong key" — not a server-side failure.
type ConnectionTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// TestConnection dispatches req to the client for its Service and makes one
// lightweight, argument-free call to confirm the URL/key actually work.
//
// Only services with an existing, argument-free read call are supported so
// far (Radarr/Sonarr's root-folder list, Ollama's model list, Stash's tag
// list). StashDB/FansDB, TPDB, and Brave don't have one in their ported
// clients yet — their public methods all require real search terms — so
// testing those needs a small new query added to each client first, not
// just wiring here.
func TestConnection(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	switch req.Service {
	case "radarr":
		return testServarr(ctx, httpClient, servarr.Radarr, req)
	case "sonarr":
		return testServarr(ctx, httpClient, servarr.Sonarr, req)
	case "ollama":
		return testOllama(ctx, httpClient, req)
	case "stash":
		return testStash(ctx, httpClient, req)
	default:
		return ConnectionTestResult{Error: fmt.Sprintf("unsupported service %q", req.Service)}
	}
}

func testServarr(ctx context.Context, httpClient *http.Client, app servarr.App, req ConnectionTestRequest) ConnectionTestResult {
	c := servarr.New(servarr.Config{BaseURL: req.URL, APIKey: req.APIKey, App: app}, httpClient)
	if _, err := c.RootFolders(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

func testOllama(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := ollama.New(req.URL, "", httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// testStash expects req.URL to already point at Stash's GraphQL endpoint
// (e.g. "http://host:9999/graphql"), matching stashapi.Config.URL.
func testStash(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	c := stashapi.New(stashapi.Config{URL: req.URL, APIKey: req.APIKey}, httpClient)
	if _, err := c.AllTags(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}
