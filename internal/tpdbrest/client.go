// Package tpdbrest is a minimal client for ThePornDB's REST API — used as a
// fallback where its GraphQL endpoint (see internal/stashbox) doesn't cover a
// lookup (e.g. hash-based search), and for title text search.
package tpdbrest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	return &Client{baseURL: baseURL, apiKey: apiKey, http: httpClient}
}

// Scene mirrors a subset of ThePornDB's REST scene response shape.
type Scene struct {
	ID    string
	Title string
	Date  string
	Site  string // studio name
	Image string // scene thumbnail/poster URL (may be empty; see rawScene.Image)
}

type rawSite struct {
	Name string `json:"name"`
}

// rawScene mirrors the fields this client consumes from a TPDB v2 scene object.
// Image is TPDB's top-level "image" field — the primary scene still/poster URL,
// served from TPDB's own image CDN (cdn.theporndb.net, and legacy
// cdn.metadataapi.net — both subdomains of the domains internal/imageproxy
// already allowlists). The scene object also carries poster_image/poster and a
// posters[] array, but the flat "image" field is the one universally present
// and is what the Discover thumbnail uses. It can be empty for scenes with no
// art, so consumers must degrade gracefully. Anchored to TPDB's documented v2
// scene shape (Jellyfin/Plex TPDB agents, community Go clients); the field is
// modeled from that documentation, not confirmed against a live authenticated
// instance in-repo.
type rawScene struct {
	ID    string   `json:"_id"`
	Title string   `json:"title"`
	Date  string   `json:"date"`
	Site  *rawSite `json:"site"`
	Image string   `json:"image"`
}

func (s rawScene) toScene() Scene {
	site := ""
	if s.Site != nil {
		site = s.Site.Name
	}
	return Scene{ID: s.ID, Title: s.Title, Date: s.Date, Site: site, Image: s.Image}
}

type scenesResponse struct {
	Data []rawScene `json:"data"`
}

// doGet is the shared GET+decode mechanics every REST lookup (scenes,
// performers, sites) uses — path-scoped so each gets its own typed wrapper
// below rather than every caller reaching into a shared /scenes endpoint.
func (c *Client) doGet(ctx context.Context, path string, params url.Values, out any) error {
	u := c.baseURL + path + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, out)
}

func (c *Client) get(ctx context.Context, params url.Values) ([]Scene, error) {
	var sr scenesResponse
	if err := c.doGet(ctx, "/scenes", params, &sr); err != nil {
		return nil, err
	}
	out := make([]Scene, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = rs.toScene()
	}
	return out, nil
}

// Ping confirms the base URL/key work by making one real, minimal request
// against the same /scenes endpoint SearchByHash and SearchByTitle use —
// ThePornDB's REST API has no separate lightweight "verify key" endpoint, so
// a trivially-scoped real call (per_page=1, no search term) is the honest
// check: it 401s on a bad key exactly like a real search would, without
// asserting anything about the result content.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.get(ctx, url.Values{"per_page": {"1"}})
	return err
}

// defaultBrowsePerPage is BrowseScenes' page size when the caller passes a
// non-positive per-page count — a sane default for a Discover grid.
const defaultBrowsePerPage = 20

// BrowseScenes returns one page of ThePornDB's scene catalog with NO search
// term — the plain paginated browse backing Adult's Discover screen, reusing
// the exact bare-pagination call shape Ping already proved works (per_page/page,
// no q). page and perPage are clamped to sane minimums (page >= 1; perPage
// defaults to defaultBrowsePerPage when non-positive) so a bad client value can
// never produce a malformed query. Ordering/trending params are deliberately
// NOT sent — v1 is plain browse only (see the plan's Stage 7 deferral).
func (c *Client) BrowseScenes(ctx context.Context, page, perPage int) ([]Scene, error) {
	if perPage <= 0 {
		perPage = defaultBrowsePerPage
	}
	if page <= 0 {
		page = 1
	}
	params := url.Values{
		"per_page": {strconv.Itoa(perPage)},
		"page":     {strconv.Itoa(page)},
	}
	return c.get(ctx, params)
}

// SearchByHash looks up scenes by perceptual hash (TPDB's GraphQL fingerprint
// lookup is tried first by callers; this REST fallback covers what it misses).
func (c *Client) SearchByHash(ctx context.Context, phash string) ([]Scene, error) {
	params := url.Values{"hash": {phash}, "hash_type": {"phash"}}
	return c.get(ctx, params)
}

// SearchByTitle text-searches by title, optionally narrowed by site (studio).
// Similarity filtering of results is business logic that belongs in
// internal/identify, not here.
func (c *Client) SearchByTitle(ctx context.Context, title, site string) ([]Scene, error) {
	params := url.Values{"q": {title}, "per_page": {"5"}}
	if site != "" {
		params.Set("site", site)
	}
	return c.get(ctx, params)
}

// Performer mirrors a subset of ThePornDB's REST performer response shape.
type Performer struct {
	ID   string
	Name string
}

type rawPerformer struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}

type performersResponse struct {
	Data []rawPerformer `json:"data"`
}

// SearchPerformers text-searches performers by name. Similarity filtering of
// results is business logic that belongs in internal/identify, not here —
// same convention as SearchByTitle.
func (c *Client) SearchPerformers(ctx context.Context, term string) ([]Performer, error) {
	var pr performersResponse
	if err := c.doGet(ctx, "/performers", url.Values{"q": {term}}, &pr); err != nil {
		return nil, err
	}
	out := make([]Performer, len(pr.Data))
	for i, rp := range pr.Data {
		out[i] = Performer{ID: rp.ID, Name: rp.Name}
	}
	return out, nil
}

// Site mirrors a subset of ThePornDB's REST site (studio) response shape.
type Site struct {
	ID   string
	Name string
}

type rawSiteEntry struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}

type sitesResponse struct {
	Data []rawSiteEntry `json:"data"`
}

// SearchSites text-searches sites (studios) by name.
func (c *Client) SearchSites(ctx context.Context, term string) ([]Site, error) {
	var sr sitesResponse
	if err := c.doGet(ctx, "/sites", url.Values{"q": {term}}, &sr); err != nil {
		return nil, err
	}
	out := make([]Site, len(sr.Data))
	for i, rs := range sr.Data {
		out[i] = Site{ID: rs.ID, Name: rs.Name}
	}
	return out, nil
}
