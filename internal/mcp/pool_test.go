package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMCPHelper(t *testing.T) {
	if os.Getenv("KK_MCP_TEST") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if request.ID == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "get_frames", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"times": map[string]any{"type": "array", "items": map[string]any{"type": "number", "minimum": 0}, "maxItems": 12}}, "required": []string{"times"}, "additionalProperties": false}}}}
		case "tools/call":
			args, _ := request.Params["arguments"].(map[string]any)
			times, _ := args["times"].([]any)
			if len(times) > 0 && times[0] == float64(99) {
				time.Sleep(60 * time.Second)
			}
			fmt.Fprint(os.Stderr, strings.Repeat("x", 100000))
			result = model.ToolResult{Content: []model.Block{{Type: "image", MIME: "image/png", Data: "aW1hZ2U="}}}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		fmt.Println(string(b))
	}
	os.Exit(0)
}
func scope(t *testing.T, id string) model.Scope {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return model.Scope{ID: id, Phase: "reverse", MCP: model.MCPConfig{Command: exe, Args: []string{"-test.run=^TestMCPHelper$"}, Env: map[string]string{"KK_MCP_TEST": "1"}, StartupMS: 10000}}
}
func TestBoundedPoolSwitchesScopesSpoolsImagesAndKillsTimeout(t *testing.T) {
	assets, _ := asset.New(t.TempDir(), 1<<20)
	pool := New(1, 1<<20, 1<<20, assets)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			s := scope(t, id)
			result, err := pool.Execute(ctx, s, model.ToolCall{ID: id, Name: "get_frames", Arguments: json.RawMessage(`{"times":[1]}`)})
			if err != nil {
				t.Error(err)
				return
			}
			if len(result.Content) != 1 || result.Content[0].Asset == "" || result.Content[0].Data != "" {
				t.Error("image was not spooled")
			}
		}(id)
	}
	wg.Wait()
	pool.mu.Lock()
	count := len(pool.entries)
	pool.mu.Unlock()
	if count > 1 {
		t.Fatal("exceeded worker limit")
	}
	s := scope(t, "slow")
	if _, err := pool.List(ctx, s); err != nil {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stop()
	_, err := pool.Execute(short, s, model.ToolCall{ID: "slow", Name: "get_frames", Arguments: json.RawMessage(`{"times":[99]}`)})
	if err == nil {
		t.Fatal("timeout accepted")
	}
	pool.mu.Lock()
	count = len(pool.entries)
	pool.mu.Unlock()
	if count != 0 {
		t.Fatal("cancelled worker retained")
	}
}
func TestToolSchemaRejectsInvalidArgsBeforeExecuting(t *testing.T) {
	assets, _ := asset.New(t.TempDir(), 1<<20)
	pool := New(1, 1<<20, 0, assets)
	defer pool.Close()
	result, err := pool.Execute(context.Background(), scope(t, "bad"), model.ToolCall{ID: "bad", Name: "get_frames", Arguments: json.RawMessage(`{"times":[-1]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("invalid frame request accepted")
	}
}
