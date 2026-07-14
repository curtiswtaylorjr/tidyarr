package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/curtiswtaylorjr/sakms/internal/trakt"
)

// This file is self-contained by design (task #9): every handler here is a
// plain, already-package-local `api` function, so wiring it into the real
// mux (internal/api/handler.go, owned by task #5) is a one-line
// mux.HandleFunc call with no import needed — same package, no apidto
// dependency either (this file's request/response shapes are local structs
// mirroring the existing ConnectionTestRequest/Result convention; apidto
// mirrors them separately for TS codegen, same as every other connection
// type already does). See this package's connections.go/handler.go for the
// precedent this file follows.
//
// Every handler below builds its own *trakt.Client per request from
// whatever credentials are currently in trakt.Store, rather than holding a
// long-lived *trakt.Client/*trakt.Session — client_id/secret can change at
// any time via saveTraktCredentialsHandler, and a stale cached Client would
// silently keep using the old pair. baseURL is trakt.DefaultBaseURL in every
// production call site; each public handler has a *WithBaseURL sibling so
// tests can still point it at a fake server (see this project's house HTTP
// client convention: an explicit Config.BaseURL, never a literal baked into
// the call site).
//
// Route shape (authoritative contract, adopted from worker-5's already-
// tested frontend — supersedes an earlier draft that incorrectly merged
// device-poll and general-status into one endpoint):
//
//	GET  /api/trakt/status          -> traktConnectionSummaryHandler
//	PUT  /api/trakt/credentials     -> saveTraktCredentialsHandler
//	POST /api/trakt/device/start    -> startTraktDeviceFlowHandler
//	POST /api/trakt/device/poll     -> traktDeviceFlowStatusHandler
//	POST /api/trakt/disconnect      -> traktDisconnectHandler
//	GET  /api/trakt/watchlist       -> traktWatchlistHandler
//
// plus TestConnection's "trakt" case (testTrakt), unrelated to this route
// table since it's dispatched from the existing /api/connections/test route.
//
// NOTE on /api/trakt/device/poll: it is POST, not GET, and its handler
// builds a fresh *trakt.Client per request from the store rather than
// taking a pre-built *trakt.Client — both deliberate (see each handler's
// doc comment below) — flagged here because an earlier integration request
// asked for GET + a long-lived *trakt.Client param instead.

// testTrakt is TestConnection's "trakt" case content — mirrors
// testTMDB/testOllama/etc.'s shape exactly: takes the existing
// ConnectionTestRequest unchanged (no new fields) and returns the existing
// ConnectionTestResult. Trakt's ConnectionTestRequest has no dedicated
// client_id field, so by convention the generic APIKey field carries
// client_id here (client_secret isn't needed — Ping only validates
// client_id against a public, non-OAuth endpoint).
func testTrakt(ctx context.Context, httpClient *http.Client, req ConnectionTestRequest) ConnectionTestResult {
	return testTraktWithBaseURL(ctx, httpClient, trakt.DefaultBaseURL, req.APIKey)
}

// testTraktWithBaseURL is testTrakt's actual implementation, with baseURL
// broken out so tests can point it at a fake server — testTrakt itself
// always passes trakt.DefaultBaseURL, per this project's convention of a
// real Config.BaseURL rather than a literal baked into the call site.
func testTraktWithBaseURL(ctx context.Context, httpClient *http.Client, baseURL, clientID string) ConnectionTestResult {
	c := trakt.New(trakt.Config{BaseURL: baseURL, ClientID: clientID}, httpClient)
	if err := c.Ping(ctx); err != nil {
		return ConnectionTestResult{Error: err.Error()}
	}
	return ConnectionTestResult{OK: true}
}

// traktCredentialsRequest is PUT /api/trakt/credentials's body — same
// three-state ClientSecret convention as upsertConnectionRequest.APIKey in
// handler.go (nil = preserve stored secret, "" = clear, non-empty = set).
type traktCredentialsRequest struct {
	ClientID     string  `json:"clientId"`
	ClientSecret *string `json:"clientSecret,omitempty"`
}

// saveTraktCredentialsHandler persists the operator-entered Trakt
// application (client_id/client_secret). Doesn't touch any linked account's
// tokens — see trakt.Store.SaveCredentials.
func saveTraktCredentialsHandler(store *trakt.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req traktCredentialsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.ClientID == "" {
			http.Error(w, "clientId is required", http.StatusBadRequest)
			return
		}
		if err := store.SaveCredentials(r.Context(), req.ClientID, req.ClientSecret); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// traktConnectionSummary is GET /api/trakt/status's response — the general
// "is Trakt usable right now" summary consumed both by Settings (to render
// configured/linked state and pre-fill the credential form) and by the
// Discover watchlist row (to decide whether to render at all). ClientID is
// not secret — Trakt sends it as a plain header on every request, same
// non-secret status as connections.Summary's URL field — included so
// Settings.tsx can pre-fill the client_id input; ClientSecret/AccessToken/
// RefreshToken never appear here, only booleans/timestamps derived from
// them. (ClientID added 2026-07-14 after worker-1 found Settings.tsx reads
// status.clientId to pre-fill the form.)
type traktConnectionSummary struct {
	Configured     bool   `json:"configured"`
	Linked         bool   `json:"linked"`
	ClientID       string `json:"clientId,omitempty"`
	TokenExpiresAt string `json:"tokenExpiresAt,omitempty"`
}

// traktConnectionSummaryHandler returns the current Trakt connection state
// (route: GET /api/trakt/status). An unconfigured connection is not an
// error — it returns the zero-value summary (Configured: false).
func traktConnectionSummaryHandler(store *trakt.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := store.Get(r.Context())
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, trakt.ErrNotConfigured) {
			json.NewEncoder(w).Encode(traktConnectionSummary{})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		summary := traktConnectionSummary{
			Configured: true,
			Linked:     conn.Tokens.Linked(),
			ClientID:   conn.ClientID,
		}
		if !conn.ExpiresAt.IsZero() {
			summary.TokenExpiresAt = conn.ExpiresAt.UTC().Format(time.RFC3339)
		}
		json.NewEncoder(w).Encode(summary)
	}
}

// traktDisconnectHandler unlinks the Trakt account (clears tokens) while
// leaving the operator-entered app credentials in place, so re-linking
// doesn't require re-entering client_id/secret — a normal "disconnect
// account" action, distinct from forgetting the app entirely (that would be
// store.Delete, not exposed here; not part of this contract).
func traktDisconnectHandler(store *trakt.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.ClearTokens(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// traktDeviceFlow holds the one in-flight device-code authorization (if
// any) between a start call and however many POST /api/trakt/device/poll
// calls the frontend makes on a client-side timer — necessary because the
// device flow is inherently two-step (RequestDeviceCode, then repeated
// PollDeviceToken) and the frontend can't be trusted/expected to hold the
// device_code itself across polls. Deliberately separate from
// traktConnectionSummaryHandler above (that's the general "is Trakt usable"
// check used everywhere; this is Connect-flow-only, in-progress-
// authorization state).
// A single mutex-guarded field is correct, not a premature simplification,
// because this project is single-operator/single-connection throughout
// (CLAUDE.md) — there is never more than one Trakt account being linked at
// a time. The zero value (&traktDeviceFlow{}) is ready to use.
type traktDeviceFlow struct {
	mu     sync.Mutex
	device *trakt.DeviceCode
}

// newTraktDeviceFlow is a constructor for clarity at the call site (handler.go
// wiring); the zero value would work identically.
func newTraktDeviceFlow() *traktDeviceFlow {
	return &traktDeviceFlow{}
}

// errNoTraktDeviceFlow is returned by traktDeviceFlow.poll when the
// frontend polls before ever calling start (or after the server restarted
// and lost the in-memory pending code) — the frontend's response should
// prompt the operator to start over.
var errNoTraktDeviceFlow = errors.New("trakt: no device authorization in progress; start one first")

func (f *traktDeviceFlow) start(ctx context.Context, client *trakt.Client) (*trakt.DeviceCode, error) {
	dc, err := client.RequestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.device = dc
	f.mu.Unlock()
	return dc, nil
}

// traktDeviceStatus is one of the four values traktDeviceFlowStatusHandler's
// JSON response reports.
type traktDeviceStatus string

const (
	traktDeviceStatusPending traktDeviceStatus = "pending"
	traktDeviceStatusLinked  traktDeviceStatus = "linked"
	traktDeviceStatusExpired traktDeviceStatus = "expired"
	traktDeviceStatusDenied  traktDeviceStatus = "denied"
)

// poll makes exactly one PollDeviceToken attempt against whatever device
// code is currently pending (never loops/sleeps itself — this handler is
// non-blocking by design, so polling cadence is the frontend's job: an
// interval timer respecting the start response's interval/expiresIn calling
// POST /api/trakt/device/poll repeatedly). On success, tokens are saved via
// store and the pending code is cleared. On a terminal outcome
// (expired/denied), the pending code is cleared too, so a subsequent poll
// correctly reports errNoTraktDeviceFlow instead of re-polling a dead code.
// Pending/slow-down leaves the code in place for the next poll.
func (f *traktDeviceFlow) poll(ctx context.Context, client *trakt.Client, store *trakt.Store) (traktDeviceStatus, error) {
	f.mu.Lock()
	dc := f.device
	f.mu.Unlock()
	if dc == nil {
		return "", errNoTraktDeviceFlow
	}

	tok, err := client.PollDeviceToken(ctx, dc.DeviceCode)
	switch {
	case err == nil:
		if serr := store.SaveTokens(ctx, tok.AccessToken, tok.RefreshToken, tok.ExpiresAt); serr != nil {
			return "", fmt.Errorf("saving trakt tokens: %w", serr)
		}
		f.clear()
		return traktDeviceStatusLinked, nil
	case errors.Is(err, trakt.ErrAuthorizationPending), errors.Is(err, trakt.ErrSlowDown):
		return traktDeviceStatusPending, nil
	case errors.Is(err, trakt.ErrDeviceCodeExpired):
		f.clear()
		return traktDeviceStatusExpired, nil
	case errors.Is(err, trakt.ErrDeviceCodeDenied), errors.Is(err, trakt.ErrDeviceCodeNotFound), errors.Is(err, trakt.ErrDeviceCodeUsed):
		f.clear()
		return traktDeviceStatusDenied, nil
	default:
		return "", err
	}
}

func (f *traktDeviceFlow) clear() {
	f.mu.Lock()
	f.device = nil
	f.mu.Unlock()
}

// traktClientFromStore loads the currently-stored credentials and builds a
// *trakt.Client from them. Returns trakt.ErrNotConfigured unchanged if
// nothing has been saved yet, so callers can 412 with a clear message
// instead of a generic 500.
func traktClientFromStore(ctx context.Context, store *trakt.Store, httpClient *http.Client, baseURL string) (*trakt.Client, error) {
	conn, err := store.Get(ctx)
	if err != nil {
		return nil, err
	}
	return trakt.New(trakt.Config{BaseURL: baseURL, ClientID: conn.ClientID, ClientSecret: conn.ClientSecret}, httpClient), nil
}

// traktDeviceStartResponse is POST /api/trakt/device/start's response —
// everything the frontend needs to show the operator (a code to enter and
// a URL to visit) and to know how often to call POST /api/trakt/device/poll.
// DeviceCode itself (the secret the server polls with) is deliberately NOT
// included — the frontend never needs it, since polling is server-side.
type traktDeviceStartResponse struct {
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

// startTraktDeviceFlowHandler starts a new device-code authorization.
// Returns 412 Precondition Failed if no client_id/secret has been saved yet
// — there's nothing to authorize against.
func startTraktDeviceFlowHandler(store *trakt.Store, flow *traktDeviceFlow, httpClient *http.Client) http.HandlerFunc {
	return startTraktDeviceFlowHandlerWithBaseURL(store, flow, httpClient, trakt.DefaultBaseURL)
}

func startTraktDeviceFlowHandlerWithBaseURL(store *trakt.Store, flow *traktDeviceFlow, httpClient *http.Client, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, err := traktClientFromStore(ctx, store, httpClient, baseURL)
		if errors.Is(err, trakt.ErrNotConfigured) {
			http.Error(w, "trakt is not configured yet", http.StatusPreconditionFailed)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		dc, err := flow.start(ctx, client)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(traktDeviceStartResponse{
			UserCode:        dc.UserCode,
			VerificationURL: dc.VerificationURL,
			ExpiresIn:       dc.ExpiresIn,
			Interval:        dc.Interval,
		})
	}
}

// traktDevicePollResponse is POST /api/trakt/device/poll's response.
// {linked, pending} rather than the internal 4-state traktDeviceStatus
// directly — worker-5's tested frontend polling loop only ever branches on
// `linked` (success) vs. not, so `linked`/`pending` is the wire contract;
// expired and denied both collapse to {linked:false, pending:false}. No
// information the frontend actually reads is lost: the frontend has its
// own client-side deadline for "expired", and a poll after either terminal
// state clears (errNoTraktDeviceFlow, 409) is what surfaces "start over."
// (Fixed 2026-07-14 after worker-1 found the mismatch against the real
// frontend/src/api/trakt.ts — the original {status: string} shape here was
// never read by the frontend, so a real link would poll forever.)
type traktDevicePollResponse struct {
	Linked  bool `json:"linked"`
	Pending bool `json:"pending"`
}

// traktDeviceFlowStatusHandler makes one poll attempt and reports the
// outcome. Returns 409 Conflict if no flow is in progress (start wasn't
// called, or the pending code was already resolved/cleared by an earlier
// poll). Wired as POST /api/trakt/device/poll — intentionally a separate
// endpoint from traktConnectionSummaryHandler (GET /api/trakt/status): one
// drives the Connect-flow UI's polling loop (and has a side effect on
// success — persisting tokens — hence POST, not GET), the other answers "is
// Trakt usable right now" everywhere else; conflating them was an earlier
// draft's mistake, corrected per the authoritative contract. Builds its own
// *trakt.Client per call (via traktClientFromStore) rather than taking one
// as a parameter, for the same staleness reason as every other handler in
// this file — a client_id/secret update mid-flow must be picked up on the
// very next poll, not require a server restart.
func traktDeviceFlowStatusHandler(store *trakt.Store, flow *traktDeviceFlow, httpClient *http.Client) http.HandlerFunc {
	return traktDeviceFlowStatusHandlerWithBaseURL(store, flow, httpClient, trakt.DefaultBaseURL)
}

func traktDeviceFlowStatusHandlerWithBaseURL(store *trakt.Store, flow *traktDeviceFlow, httpClient *http.Client, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, err := traktClientFromStore(ctx, store, httpClient, baseURL)
		if errors.Is(err, trakt.ErrNotConfigured) {
			http.Error(w, "trakt is not configured yet", http.StatusPreconditionFailed)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status, err := flow.poll(ctx, client, store)
		if errors.Is(err, errNoTraktDeviceFlow) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(traktDevicePollResponse{
			Linked:  status == traktDeviceStatusLinked,
			Pending: status == traktDeviceStatusPending,
		})
	}
}

// traktWatchlistItem is one entry of GET /api/trakt/watchlist's response —
// a near-direct mirror of internal/trakt.WatchlistItem's fields (Type is
// Trakt's own "movie"/"show" value, unconverted; TMDBID becomes tmdbId).
// Per the authoritative contract this is deliberately NOT DiscoverItem's
// shape — no posterPath/overview/voteAverage — since Trakt's watchlist API
// doesn't provide artwork/overview/rating at all; any enrichment against
// TMDB using tmdbId is left to the frontend/task #5, not done here (an
// N-item watchlist would mean N extra TMDB calls per page load if done
// server-side).
type traktWatchlistItem struct {
	Type   string `json:"type"` // "movie" or "show"
	Title  string `json:"title"`
	Year   int    `json:"year,omitempty"`
	TMDBID int    `json:"tmdbId"`
}

// traktWatchlistHandler returns the linked account's watchlist. Not
// configured or not yet linked both degrade to an empty list (not an
// error) — the watchlist row simply has nothing to show until GET
// /api/trakt/status reports configured/linked; a 4xx here would just be
// extra error-handling the frontend doesn't need for a read-only row. Any
// other failure (e.g. Trakt itself erroring) is a real 502, since that's an
// actual fetch failure worth surfacing. Builds its own *trakt.Session per
// request (via traktClientFromStore) rather than taking one as a parameter
// — same staleness reasoning as every other handler in this file, and it
// also means callers don't need to manage a long-lived Session at wiring
// time either.
func traktWatchlistHandler(store *trakt.Store, httpClient *http.Client) http.HandlerFunc {
	return traktWatchlistHandlerWithBaseURL(store, httpClient, trakt.DefaultBaseURL)
}

func traktWatchlistHandlerWithBaseURL(store *trakt.Store, httpClient *http.Client, baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		client, err := traktClientFromStore(ctx, store, httpClient, baseURL)
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, trakt.ErrNotConfigured) {
			json.NewEncoder(w).Encode([]traktWatchlistItem{})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		session := trakt.NewSession(store, client)
		items, err := session.Watchlist(ctx)
		if errors.Is(err, trakt.ErrNotLinked) {
			json.NewEncoder(w).Encode([]traktWatchlistItem{})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out := make([]traktWatchlistItem, len(items))
		for i, it := range items {
			out[i] = traktWatchlistItem{Type: it.Type, Title: it.Title, Year: it.Year, TMDBID: it.TMDBID}
		}
		json.NewEncoder(w).Encode(out)
	}
}
