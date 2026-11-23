package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"kkagent/internal/agent"
	"kkagent/internal/asset"
	"kkagent/internal/config"
	"kkagent/internal/mcp"
	"kkagent/internal/model"
	"kkagent/internal/provider"
	"kkagent/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPAuthIdempotencySSEReplayAndRestart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.AuthToken = "local-test-token"
	assets, _ := asset.New(filepath.Join(cfg.DataDir, "assets"), 1<<20)
	disk, _ := store.New(cfg.DataDir)
	newEngine := func() *agent.Engine {
		tools := mcp.New(1, 1<<20, 0, assets)
		engine, err := agent.New(cfg, provider.New(assets, 1<<20), tools, assets, disk)
		if err != nil {
			t.Fatal(err)
		}
		return engine
	}
	engine := newEngine()
	defer engine.Close()
	server := httptest.NewServer((&Server{Engine: engine, Config: cfg}).Handler())
	defer server.Close()
	body := `{"client_request_id":"http-once","session_id":"http","task":"hi","provider":{"protocol":"openai-completions","base_url":"` + upstream.URL + `","model":"local","api_key":"not-persisted-secret"}}`
	call := func(method, path, body, token, origin string) *http.Response {
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	denied := call("POST", "/v1/runs", body, "", "")
	denied.Body.Close()
	if denied.StatusCode != 401 {
		t.Fatal("missing token accepted")
	}
	denied = call("POST", "/v1/runs", body, cfg.AuthToken, "http://third-party.example")
	denied.Body.Close()
	if denied.StatusCode != 403 {
		t.Fatal("cross-origin request accepted")
	}
	created := call("POST", "/v1/runs", body, cfg.AuthToken, "")
	var snapshot model.Snapshot
	if err := json.NewDecoder(created.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if created.StatusCode != 202 {
		t.Fatalf("create failed %d %+v", created.StatusCode, snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := engine.Wait(ctx, snapshot.ID)
	if err != nil || done.Status != "completed" {
		t.Fatalf("failed: %+v %v", done, err)
	}
	sse := call("GET", "/v1/runs/"+snapshot.ID+"/events?after=1", "", cfg.AuthToken, "")
	raw, err := io.ReadAll(sse.Body)
	sse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "id: 1\n") || !strings.Contains(string(raw), "event: turn/end") {
		t.Fatalf("invalid SSE replay %s", raw)
	}
	engine.Close()
	restarted := newEngine()
	defer restarted.Close()
	var spec model.Spec
	_ = json.Unmarshal([]byte(body), &spec)
	same, err := restarted.Start(spec)
	if err != nil || same.ID != snapshot.ID {
		t.Fatalf("restart lost idempotency: %+v %v", same, err)
	}
	persisted, _ := json.Marshal(same)
	if strings.Contains(string(persisted), "not-persisted-secret") {
		t.Fatal("credential leaked")
	}
}
