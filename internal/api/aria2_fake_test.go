package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/curtiswtaylorjr/sakms/internal/downloader"
)

// fakeAria2Server is a JSON-RPC server standing in for aria2c in the download
// handler tests. addGID is what aria2.addUri returns; completed maps a GID to
// the tellStopped status struct reported for it (all numeric fields are the
// decimal STRINGS aria2 actually sends on the wire). A completed download's
// files/dir are what checkImportHandler / the onComplete callback relocate.
type fakeAria2Server struct {
	mu        sync.Mutex
	addGID    string
	addedURIs []string
	completed map[string]map[string]any
	active    map[string]map[string]any
}

// newFakeAria2 starts a fake aria2 JSON-RPC server returning addGID from
// aria2.addUri. Register completed downloads with setCompleteDir/setCompleteFile
// before hitting check-import.
func newFakeAria2(t *testing.T, addGID string) (*httptest.Server, *fakeAria2Server) {
	t.Helper()
	f := &fakeAria2Server{addGID: addGID, completed: map[string]map[string]any{}, active: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		var result interface{}
		switch req.Method {
		case "aria2.addUri":
			// params can be [token?, uris[], options?] — the uris array is the
			// first param that decodes as a JSON array; capture its first entry.
			for _, p := range req.Params {
				if uris, ok := p.([]interface{}); ok && len(uris) > 0 {
					if s, ok := uris[0].(string); ok {
						f.addedURIs = append(f.addedURIs, s)
					}
					break
				}
			}
			result = f.addGID
		case "aria2.tellActive":
			items := []interface{}{}
			for _, s := range f.active {
				items = append(items, s)
			}
			result = items
		case "aria2.tellWaiting":
			result = []interface{}{}
		case "aria2.tellStopped":
			items := []interface{}{}
			for _, s := range f.completed {
				items = append(items, s)
			}
			result = items
		default:
			result = "OK"
		}
		raw, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(map[string]interface{}{"result": json.RawMessage(raw), "id": "sakms"})
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

// setCompleteDir marks gid complete with dir as its staging directory and NO
// individual files — so checkImportHandler's contentPath falls back to dir,
// matching the old qBittorrent content_path=directory relocate behavior (the
// whole directory tree is moved).
func (f *fakeAria2Server) setCompleteDir(gid, dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed[gid] = map[string]any{
		"gid": gid, "status": "complete", "totalLength": "100", "completedLength": "100",
		"dir": dir, "files": []map[string]any{},
	}
}

// setActive marks gid as an active (still-downloading) item at 50% progress.
func (f *fakeAria2Server) setActive(gid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[gid] = map[string]any{
		"gid": gid, "status": "active", "totalLength": "100", "completedLength": "50",
		"downloadSpeed": "1024", "connections": "4",
	}
}

// newTestDownloader builds a Manager pointing at a fake aria2 server, with
// staging dir set to stagingDir (used as the onComplete/import fallback path).
func newTestDownloader(aria2URL, stagingDir string) *downloader.Manager {
	return downloader.NewForTesting(aria2URL, stagingDir, testHTTPClient())
}
