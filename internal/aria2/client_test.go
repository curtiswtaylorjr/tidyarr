package aria2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// decodeReq reads the JSON-RPC request the client sent, so a test can assert
// on the method and params it produced.
func decodeReq(t *testing.T, r *http.Request) rpcRequest {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("reading request body: %v", err)
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decoding request: %v", err)
	}
	return req
}

func writeResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshaling result: %v", err)
	}
	json.NewEncoder(w).Encode(rpcResponse{Result: raw})
}

func TestAria2Client_AddTorrent(t *testing.T) {
	var gotMethod string
	var gotParams []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeReq(t, r)
		gotMethod = req.Method
		gotParams = req.Params
		writeResult(t, w, "2089b05ecca3d829")
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL, Token: "sekret"}, http.DefaultClient)
	gid, err := c.AddTorrent(context.Background(), "magnet:?xt=urn:btih:abc", "/staging")
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}
	if gid != "2089b05ecca3d829" {
		t.Errorf("gid = %q, want 2089b05ecca3d829", gid)
	}
	if gotMethod != "aria2.addUri" {
		t.Errorf("method = %q, want aria2.addUri", gotMethod)
	}
	// First param must be the token (Config.Token set), then the uris array,
	// then the dir options struct.
	if len(gotParams) != 3 {
		t.Fatalf("params len = %d, want 3 (token, uris, options): %v", len(gotParams), gotParams)
	}
	if gotParams[0] != "token:sekret" {
		t.Errorf("param[0] = %v, want token:sekret", gotParams[0])
	}
}

func TestAria2Client_AddNZB_Unsupported(t *testing.T) {
	c := New(Config{Endpoint: "http://unused"}, http.DefaultClient)
	if _, err := c.AddNZB(context.Background(), []byte("nzb"), "/staging"); err != ErrUsenetUnsupported {
		t.Errorf("AddNZB err = %v, want ErrUsenetUnsupported", err)
	}
}

func TestAria2Client_TellActive_ParsesStringNumerics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeReq(t, r)
		if req.Method != "aria2.tellActive" {
			t.Errorf("method = %q, want aria2.tellActive", req.Method)
		}
		writeResult(t, w, []map[string]any{{
			"gid":             "2089b05ecca3d829",
			"status":          "active",
			"totalLength":     "100000000",
			"completedLength": "50000000",
			"downloadSpeed":   "10240",
			"connections":     "10",
			"dir":             "/staging",
			"files":           []map[string]any{{"path": "/staging/movie.mkv"}},
		}})
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL}, http.DefaultClient)
	dls, err := c.TellActive(context.Background())
	if err != nil {
		t.Fatalf("TellActive: %v", err)
	}
	if len(dls) != 1 {
		t.Fatalf("got %d downloads, want 1", len(dls))
	}
	d := dls[0]
	if d.GID != "2089b05ecca3d829" || d.Status != "active" {
		t.Errorf("gid/status = %q/%q", d.GID, d.Status)
	}
	if d.TotalLength != 100000000 || d.CompletedLength != 50000000 {
		t.Errorf("lengths = %d/%d, want 100000000/50000000", d.TotalLength, d.CompletedLength)
	}
	if d.DownloadSpeed != 10240 || d.Connections != 10 {
		t.Errorf("speed/conns = %d/%d, want 10240/10", d.DownloadSpeed, d.Connections)
	}
	if len(d.Files) != 1 || d.Files[0] != "/staging/movie.mkv" {
		t.Errorf("files = %v", d.Files)
	}
	if d.Filename != "/staging/movie.mkv" {
		t.Errorf("filename = %q", d.Filename)
	}
}

func TestAria2Client_GetGlobalStat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResult(t, w, map[string]any{
			"numActive": "2", "numWaiting": "1", "numStopped": "3",
			"downloadSpeed": "21846", "uploadSpeed": "0",
		})
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL}, http.DefaultClient)
	stat, err := c.GetGlobalStat(context.Background())
	if err != nil {
		t.Fatalf("GetGlobalStat: %v", err)
	}
	if stat.NumActive != 2 || stat.NumWaiting != 1 || stat.NumStopped != 3 {
		t.Errorf("counts = %d/%d/%d, want 2/1/3", stat.NumActive, stat.NumWaiting, stat.NumStopped)
	}
	if stat.DownloadSpeed != 21846 {
		t.Errorf("downloadSpeed = %d, want 21846", stat.DownloadSpeed)
	}
}

func TestAria2Client_RPCError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(rpcResponse{Error: &rpcError{Code: 1, Message: "boom"}})
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL}, http.DefaultClient)
	if err := c.RemoveDownload(context.Background(), "gid"); err == nil {
		t.Error("expected an rpc error, got nil")
	}
}

func TestAria2Client_PauseUnpauseRemove(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, decodeReq(t, r).Method)
		writeResult(t, w, "gid")
	}))
	defer srv.Close()

	c := New(Config{Endpoint: srv.URL}, http.DefaultClient)
	ctx := context.Background()
	if err := c.PauseDownload(ctx, "g"); err != nil {
		t.Fatalf("PauseDownload: %v", err)
	}
	if err := c.UnpauseDownload(ctx, "g"); err != nil {
		t.Fatalf("UnpauseDownload: %v", err)
	}
	if err := c.RemoveDownloadResult(ctx, "g"); err != nil {
		t.Fatalf("RemoveDownloadResult: %v", err)
	}
	want := []string{"aria2.pause", "aria2.unpause", "aria2.removeDownloadResult"}
	for i, m := range want {
		if methods[i] != m {
			t.Errorf("call %d method = %q, want %q", i, methods[i], m)
		}
	}
}
