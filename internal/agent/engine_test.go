package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"kkagent/internal/asset"
	"kkagent/internal/config"
	"kkagent/internal/model"
	"kkagent/internal/store"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProvider struct {
	mu       sync.Mutex
	calls    map[string]int
	block    bool
	toolMode bool
}

func (p *fakeProvider) Next(ctx context.Context, r model.Request, _ model.Delta) (model.Turn, error) {
	if p.block {
		<-ctx.Done()
		return model.Turn{}, ctx.Err()
	}
	task := r.Messages[0].Content[0].Text
	p.mu.Lock()
	if p.calls == nil {
		p.calls = map[string]int{}
	}
	p.calls[task]++
	n := p.calls[task]
	p.mu.Unlock()
	turn := model.Turn{Message: model.Message{Role: "assistant", Content: []model.Block{{Type: "text", Text: task + "-done"}}}, Finish: "completed", Usage: model.Usage{Input: 2, Output: 1, Total: 3, Known: true}, Elapsed: time.Millisecond}
	if p.toolMode && n == 1 {
		turn.Message.Calls = []model.ToolCall{{ID: "c1", Name: "get_frames", Arguments: json.RawMessage(`{"times":[1]}`)}}
		turn.Finish = "tool-calls"
	}
	if p.toolMode && n == 2 {
		last := r.Messages[len(r.Messages)-1]
		if last.Role != "tool" || last.Content[0].Asset == "" {
			return turn, errors.New("tool image not spooled")
		}
	}
	return turn, nil
}

type fakeTools struct {
	mu    sync.Mutex
	calls int
}

func (t *fakeTools) List(context.Context, model.Scope) ([]model.ToolDefinition, error) {
	return []model.ToolDefinition{{Name: "get_frames", Schema: json.RawMessage(`{"type":"object"}`)}}, nil
}
func (t *fakeTools) Execute(context.Context, model.Scope, model.ToolCall) (model.ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return model.ToolResult{Content: []model.Block{{Type: "image", Data: "aW1hZ2U=", MIME: "image/png"}}}, nil
}
func (t *fakeTools) Release(model.Scope) {}
func (t *fakeTools) Close() error        { return nil }
func newEngine(t *testing.T, p model.Provider, tools model.Tools) *Engine {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.MaxActive = 1
	cfg.MaxLLM = 1
	a, err := asset.New(filepath.Join(cfg.DataDir, "assets"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.New(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, p, tools, a, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func spec(name string) model.Spec {
	return model.Spec{ClientID: name, SessionID: name, Task: name, Provider: &model.ProviderConfig{Protocol: "openai-completions", BaseURL: "http://127.0.0.1:9/v1", Model: "test"}}
}
func wait(t *testing.T, e *Engine, id string) model.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := e.Wait(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestToolLoopPersistsAssetsUsageAndIdempotency(t *testing.T) {
	tools := &fakeTools{}
	e := newEngine(t, &fakeProvider{toolMode: true}, tools)
	s := spec("tool-task")
	s.ToolsEnabled = true
	s.Scope = &model.Scope{ID: "scope", Phase: "reverse"}
	r, err := e.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, e, r.ID)
	if done.Status != "completed" {
		t.Fatalf("failed: %+v", done)
	}
	if done.Result.Usage.Total != 6 {
		t.Fatal("did not accumulate rounds")
	}
	same, err := e.Start(s)
	if err != nil || same.ID != r.ID {
		t.Fatal("duplicate submit executed again")
	}
	tools.mu.Lock()
	count := tools.calls
	tools.mu.Unlock()
	if count != 1 {
		t.Fatalf("tool ran %d times", count)
	}
	s.Task = "changed"
	if _, err = e.Start(s); err == nil {
		t.Fatal("accepted changed idempotency request")
	}
	raw, _ := json.Marshal(done)
	if strings.Contains(string(raw), "aW1hZ2U=") {
		t.Fatal("image base64 leaked into status")
	}
	events, _ := e.Events(r.ID, 0)
	if len(events) == 0 || events[len(events)-1].Type != "turn/end" {
		t.Fatal("missing journal events")
	}
}
func TestBatchDoesNotDeadlockAtSingleConcurrencyAndOrdersResults(t *testing.T) {
	e := newEngine(t, &fakeProvider{}, &fakeTools{})
	children := []model.Spec{}
	for i := 0; i < 5; i++ {
		children = append(children, spec(fmt.Sprintf("child-%d", i)))
	}
	batch := model.Spec{ClientID: "batch", Kind: "batch", Children: children}
	r, err := e.Start(batch)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, e, r.ID)
	if done.Status != "completed" || len(done.Children) != 5 {
		t.Fatalf("batch failed: %+v", done)
	}
	if done.Result.Text != "child-0-done|\nchild-1-done|\nchild-2-done|\nchild-3-done|\nchild-4-done" {
		t.Fatal("unordered children")
	}
	if done.Result.Usage.Total != 15 {
		t.Fatal("batch usage double-counted or lost")
	}
}
func TestParentCancellationReachesWaitingChildren(t *testing.T) {
	e := newEngine(t, &fakeProvider{block: true}, &fakeTools{})
	r, err := e.Start(model.Spec{ClientID: "cancel-batch", Kind: "batch", Children: []model.Spec{spec("one"), spec("two")}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, _ := e.Get(r.ID)
		if len(s.Children) == 2 {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	_ = e.Cancel(r.ID)
	done := wait(t, e, r.ID)
	if done.Status != "cancelled" {
		t.Fatalf("unexpected parent status %s", done.Status)
	}
	for _, child := range done.Children {
		if status := wait(t, e, child).Status; status != "cancelled" {
			t.Fatalf("child not cancelled: %s", status)
		}
	}
}
