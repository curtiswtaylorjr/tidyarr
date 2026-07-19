// Package aria2 is a JSON-RPC client for a running aria2c daemon — the
// download engine SAK's unified downloader (internal/downloader) manages as
// a subprocess. It follows this project's house HTTP-client pattern (Config
// + Client{cfg, http} + New(cfg, httpClient), hand-built requests, no
// interfaces), testable via a concrete *Client against httptest.NewServer.
//
// aria2 speaks HTTP(S)/FTP/SFTP/BitTorrent/Metalink — it has NO usenet/NNTP
// support (confirmed against aria2's own JSON-RPC manual). So AddTorrent
// handles the torrent/magnet path that dispatchToDownloadClient feeds it,
// and AddNZB deliberately returns an explicit not-supported error rather
// than pretending — usenet is out of scope for the aria2 backend (see
// AddNZB's doc and the ROADMAP's "Unified downloader" entry). This is the
// house "honesty about unverified/unsupported assumptions" convention.
//
// aria2's status structs report every numeric field as a DECIMAL STRING
// (e.g. "totalLength":"100000000"), so the wire structs below decode those
// into strings and Download's int64 fields are parsed from them — a bare
// int64 json tag would fail to decode aria2's actual responses.
package aria2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/curtiswtaylorjr/sakms/internal/httpx"
)

// Config parameterizes the client for one aria2c JSON-RPC endpoint.
type Config struct {
	// Endpoint is the full JSON-RPC URL, e.g. "http://127.0.0.1:6800/jsonrpc".
	Endpoint string
	// Token is aria2's --rpc-secret value (without the "token:" prefix this
	// client adds when building params). Empty is allowed (no secret set).
	Token string
}

type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient}
}

// Download is one download's status, as reported by aria2 (tellStatus /
// tellActive / tellWaiting / tellStopped share this shape). The int64 fields
// are parsed from aria2's decimal-string wire values (see package doc).
type Download struct {
	GID             string
	Status          string // "active" | "waiting" | "paused" | "error" | "complete" | "removed"
	Filename        string
	Dir             string
	TotalLength     int64
	CompletedLength int64
	DownloadSpeed   int64
	Connections     int64
	Files           []string
	ErrorMessage    string
}

// GlobalStat is aria2's aggregate view: active/waiting/stopped counts plus
// combined speeds. Counts and speeds arrive as decimal strings too.
type GlobalStat struct {
	NumActive     int64
	NumWaiting    int64
	NumStopped    int64
	DownloadSpeed int64
	UploadSpeed   int64
}

// rpcRequest is one JSON-RPC 2.0 call. params is assembled per-method.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

// rpcResponse is the JSON-RPC 2.0 envelope. Result is left raw so each caller
// decodes the method-specific shape; Error surfaces aria2's own error.
type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("aria2 rpc error %d: %s", e.Code, e.Message) }

// call executes method with the given method-specific params (the "token:"
// secret is prepended here, so callers pass only their real params), and
// decodes aria2's result into out. out may be nil for calls whose result is
// ignored.
func (c *Client) call(ctx context.Context, method string, params []any, out any) error {
	full := make([]any, 0, len(params)+1)
	if c.cfg.Token != "" {
		full = append(full, "token:"+c.cfg.Token)
	}
	full = append(full, params...)

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: "sakms", Method: method, Params: full})
	if err != nil {
		return fmt.Errorf("marshaling %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	var envelope rpcResponse
	if err := httpx.DoJSON(c.http, req, httpx.MaxResponseBodySize, &envelope); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("decoding %s result: %w", method, err)
		}
	}
	return nil
}

// AddTorrent queues a torrent by magnet link or .torrent URL. aria2's own
// guidance is to use addUri for both magnet URIs and remote .torrent URLs
// (addTorrent is for base64 .torrent CONTENT, which SAK doesn't have here —
// dispatchToDownloadClient hands over a URL). dir sets aria2's download
// directory for this item. Returns the assigned GID.
func (c *Client) AddTorrent(ctx context.Context, uri, dir string) (string, error) {
	params := []any{[]string{uri}}
	if dir != "" {
		params = append(params, map[string]string{"dir": dir})
	}
	var gid string
	if err := c.call(ctx, "aria2.addUri", params, &gid); err != nil {
		return "", err
	}
	return gid, nil
}

// ErrUsenetUnsupported is returned by AddNZB: aria2 has no usenet/NNTP
// capability, so the aria2 backend cannot download an NZB. Callers surface
// this as an explicit "usenet not supported" message rather than a silent
// failure.
var ErrUsenetUnsupported = errors.New("aria2: usenet/NZB downloads aren't supported by the aria2 backend")

// AddNZB always returns ErrUsenetUnsupported. It exists so the download
// pipeline can dispatch on protocol without a nil-method gap, but aria2
// genuinely cannot fetch usenet content (it speaks HTTP/FTP/SFTP/BitTorrent/
// Metalink only — no NNTP, no yenc, no par2). Downloading NZBs would need a
// separate usenet engine, out of scope for this backend (see the ROADMAP's
// "Unified downloader" entry). base64-encoding the NZB and handing it to any
// aria2 method would not work — there's no aria2 method that understands the
// usenet manifest inside it.
func (c *Client) AddNZB(_ context.Context, _ []byte, _ string) (string, error) {
	return "", ErrUsenetUnsupported
}

// TellActive returns every currently-downloading item.
func (c *Client) TellActive(ctx context.Context) ([]Download, error) {
	var raw []wireDownload
	if err := c.call(ctx, "aria2.tellActive", nil, &raw); err != nil {
		return nil, err
	}
	return toDownloads(raw), nil
}

// TellWaiting returns queued (not-yet-active) items in [offset, offset+num).
func (c *Client) TellWaiting(ctx context.Context, offset, num int) ([]Download, error) {
	var raw []wireDownload
	if err := c.call(ctx, "aria2.tellWaiting", []any{offset, num}, &raw); err != nil {
		return nil, err
	}
	return toDownloads(raw), nil
}

// TellStopped returns finished/errored/removed items in [offset, offset+num).
func (c *Client) TellStopped(ctx context.Context, offset, num int) ([]Download, error) {
	var raw []wireDownload
	if err := c.call(ctx, "aria2.tellStopped", []any{offset, num}, &raw); err != nil {
		return nil, err
	}
	return toDownloads(raw), nil
}

// RemoveDownload cancels an active/waiting download. aria2 moves it to the
// stopped list with status "removed"; call RemoveDownloadResult to purge it
// from there entirely.
func (c *Client) RemoveDownload(ctx context.Context, gid string) error {
	return c.call(ctx, "aria2.remove", []any{gid}, nil)
}

// RemoveDownloadResult purges a completed/errored/removed download from the
// stopped list.
func (c *Client) RemoveDownloadResult(ctx context.Context, gid string) error {
	return c.call(ctx, "aria2.removeDownloadResult", []any{gid}, nil)
}

// PauseDownload pauses an active download (it stays in the queue as "paused").
func (c *Client) PauseDownload(ctx context.Context, gid string) error {
	return c.call(ctx, "aria2.pause", []any{gid}, nil)
}

// UnpauseDownload resumes a paused download.
func (c *Client) UnpauseDownload(ctx context.Context, gid string) error {
	return c.call(ctx, "aria2.unpause", []any{gid}, nil)
}

// GetGlobalStat returns aria2's aggregate active/waiting/stopped counts and
// combined download/upload speeds.
func (c *Client) GetGlobalStat(ctx context.Context) (GlobalStat, error) {
	var raw wireGlobalStat
	if err := c.call(ctx, "aria2.getGlobalStat", nil, &raw); err != nil {
		return GlobalStat{}, err
	}
	return GlobalStat{
		NumActive:     atoi64(raw.NumActive),
		NumWaiting:    atoi64(raw.NumWaiting),
		NumStopped:    atoi64(raw.NumStopped),
		DownloadSpeed: atoi64(raw.DownloadSpeed),
		UploadSpeed:   atoi64(raw.UploadSpeed),
	}, nil
}

// --- wire shapes (aria2 reports numbers as decimal strings) ----------------

type wireDownload struct {
	GID             string     `json:"gid"`
	Status          string     `json:"status"`
	TotalLength     string     `json:"totalLength"`
	CompletedLength string     `json:"completedLength"`
	DownloadSpeed   string     `json:"downloadSpeed"`
	Connections     string     `json:"connections"`
	Dir             string     `json:"dir"`
	ErrorMessage    string     `json:"errorMessage"`
	Files           []wireFile `json:"files"`
}

type wireFile struct {
	Path string `json:"path"`
}

type wireGlobalStat struct {
	NumActive     string `json:"numActive"`
	NumWaiting    string `json:"numWaiting"`
	NumStopped    string `json:"numStopped"`
	DownloadSpeed string `json:"downloadSpeed"`
	UploadSpeed   string `json:"uploadSpeed"`
}

// toDownloads converts aria2's string-encoded wire structs into the exported
// Download shape, parsing decimal-string numerics and flattening file paths.
func toDownloads(raw []wireDownload) []Download {
	out := make([]Download, 0, len(raw))
	for _, w := range raw {
		files := make([]string, 0, len(w.Files))
		for _, f := range w.Files {
			if f.Path != "" {
				files = append(files, f.Path)
			}
		}
		out = append(out, Download{
			GID:             w.GID,
			Status:          w.Status,
			Filename:        firstFilename(files),
			Dir:             w.Dir,
			TotalLength:     atoi64(w.TotalLength),
			CompletedLength: atoi64(w.CompletedLength),
			DownloadSpeed:   atoi64(w.DownloadSpeed),
			Connections:     atoi64(w.Connections),
			Files:           files,
			ErrorMessage:    w.ErrorMessage,
		})
	}
	return out
}

// firstFilename is the base of the first file path, "" when there are none.
func firstFilename(files []string) string {
	if len(files) == 0 {
		return ""
	}
	// Return the first file's full path — the caller (frontend) derives a
	// display basename; keeping the raw path here avoids a filepath import
	// and matches aria2's own per-file path reporting.
	return files[0]
}

// atoi64 parses a decimal string to int64, returning 0 for empty/invalid —
// aria2 always sends valid decimals, so a parse failure just degrades to 0
// rather than erroring the whole status read.
func atoi64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
