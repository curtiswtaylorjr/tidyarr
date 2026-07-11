// Package authentik is a minimal client for Authentik's RFC 7662 token-
// introspection endpoint — the sole outbound call SAK's "authentik" auth
// mode makes (internal/auth's AuthentikAuth): given a bearer token a caller
// presents, ask Authentik whether it's currently active. SAK never becomes
// an OIDC client of Authentik (no redirect/callback flow, no JWKS) — this
// package's whole surface is one POST.
//
// HONESTY NOTE (mirrors the house convention, see internal/jellyfin): the
// introspection endpoint path, "/application/o/introspect/", was confirmed
// against Authentik's official OAuth2-provider docs
// (https://docs.goauthentik.io/docs/add-secure-apps/providers/oauth2/,
// 2026-07-10) — NOT verified against a live Authentik instance. The
// client-authentication method used here (RFC 7662 §2.1 form-body params:
// token, client_id, client_secret) is modeled from the RFC, not from that
// docs page — Authentik's docs don't specify HTTP Basic vs form-body for
// this endpoint, so this is a reasonable, spec-compliant default, not a
// confirmed fact. A wrong path or wrong auth method fails closed (the
// request errors or returns non-2xx, which Introspect treats as
// active=false), so this is a correctness-of-claim issue, not a security
// gap.
package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// Config points at an Authentik instance and the OAuth2 provider (in
// Authentik's terms, an "application") SAK introspects tokens against.
type Config struct {
	URL          string // base, e.g. https://sso.zaena.us (a trailing slash is tolerated)
	ClientID     string
	ClientSecret string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// introspectResponse decodes only the one field SAK cares about — RFC 7662
// defines several other optional fields (scope, exp, sub, ...) on an active
// token, none of which SAK's single-operator model has any use for (see
// CLAUDE.md: a forward identity or an Authentik sub still maps to the one
// operator, not a permissions surface).
type introspectResponse struct {
	Active bool `json:"active"`
}

// Introspect POSTs token to {URL}/application/o/introspect/ per RFC 7662
// §2.1 (form-encoded token/client_id/client_secret) and reports whether
// Authentik considers it active right now. Any transport error, non-2xx
// response, or a decoded active:false all collapse to (false, ...) — the
// caller (internal/auth.AuthentikAuth) must treat every non-true result the
// same way and fail closed (G5).
func (c *Client) Introspect(ctx context.Context, token string) (active bool, err error) {
	endpoint := strings.TrimSuffix(c.cfg.URL, "/") + "/application/o/introspect/"

	form := url.Values{
		"token":         {token},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var resp introspectResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &resp); err != nil {
		return false, err
	}
	return resp.Active, nil
}
