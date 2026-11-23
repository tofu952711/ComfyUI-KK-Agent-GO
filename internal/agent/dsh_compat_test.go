package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"kkagent/internal/model"
	"strings"
	"testing"
	"time"
)

type dshProbeTools struct{ called string }

func (*dshProbeTools) List(context.Context, model.Scope) ([]model.ToolDefinition, error) {
	return []model.ToolDefinition{
		{Name: "get_frames", Schema: json.RawMessage(`{"type":"object","properties":{"times":{"type":"array","items":{"type":"number"}}},"required":["times"]}`)},
		{Name: "set_video_segments", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}
func (t *dshProbeTools) Execute(_ context.Context, _ model.Scope, call model.ToolCall) (model.ToolResult, error) {
	t.called = call.Name
	return model.ToolResult{Content: []model.Block{{Type: "text", Text: `{"requested_times":[1]}`},
		{Type: "image", MIME: "image/png", Data: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jJRsAAAAASUVORK5CYII="}}}, nil
}
func (*dshProbeTools) Release(model.Scope) {}
func (*dshProbeTools) Close() error        { return nil }

type dshProbeProvider struct{ rounds int }

func (p *dshProbeProvider) Next(_ context.Context, r model.Request, _ model.Delta) (model.Turn, error) {
	p.rounds++
	names := make([]string, len(r.Tools))
	for i, definition := range r.Tools {
		names[i] = definition.Name
	}
	if strings.Join(names, ",") != "list_mcp_resource_templates,list_mcp_resources,mcp__video__get_frames,read_mcp_resource" {
		return model.Turn{}, fmt.Errorf("wrong DSH tool definitions: %v", names)
	}
	if r.Messages[0].Content[0].Text != "literal user" || r.Messages[0].Content[1].Text != "literal evidence" {
		return model.Turn{}, fmt.Errorf("changed original inputs")
	}
	turn := model.Turn{Message: model.Message{Role: "assistant", Content: []model.Block{{Type: "text", Text: "done"}}},
		Finish: "completed", Usage: model.Usage{Known: true, Output: 1, Total: 1}, Elapsed: time.Millisecond}
	if p.rounds == 1 {
		turn.Message.Calls = []model.ToolCall{{ID: "frame", Name: "mcp__video__get_frames", Arguments: json.RawMessage(`{"times":[1]}`)}}
		turn.Finish = "tool-calls"
	} else {
		last := r.Messages[len(r.Messages)-1]
		if last.Role != "tool" || len(last.Content) != 1 || last.Content[0].Type != "text" {
			return model.Turn{}, fmt.Errorf("tool image leaked into DSH continuation")
		}
		if !strings.Contains(last.Content[0].Text, "Image sha256:") || !strings.Contains(last.Content[0].Text, "1x1px, image/png") {
			return model.Turn{}, fmt.Errorf("missing persisted DSH attachment description")
		}
	}
	return turn, nil
}

func TestDSHVideoCompatibilityPreservesInputsAndRoutesNamespacedMedia(t *testing.T) {
	tools := &dshProbeTools{}
	e := newEngine(t, &dshProbeProvider{}, tools)
	s := spec("dsh-probe")
	s.Task, s.Evidence = "literal user", "literal evidence"
	s.Provider.WireCompatibility = "dsh-video"
	s.ToolsEnabled, s.Scope = true, &model.Scope{ID: "video", Phase: "reverse"}
	run, err := e.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	done := wait(t, e, run.ID)
	if done.Status != "completed" {
		t.Fatalf("failed: %+v", done)
	}
	if tools.called != "get_frames" {
		t.Fatalf("MCP received namespaced tool %q", tools.called)
	}
}

func TestDSHVideoCompatibilityRejectsUnknownModesAndNonChat(t *testing.T) {
	e := newEngine(t, &fakeProvider{}, &fakeTools{})
	for _, mode := range []string{"unknown", "dsh-video"} {
		s := spec(mode)
		s.Provider.Protocol = "openai-responses"
		s.Provider.WireCompatibility = mode
		if _, err := e.Start(s); err == nil {
			t.Fatal("accepted incompatible provider mode")
		}
	}
}

func TestDSHResourceServerMustMatchVideoScope(t *testing.T) {
	e := newEngine(t, &fakeProvider{}, &fakeTools{})
	result, err := e.dshTool(context.Background(), model.Scope{}, model.ToolCall{Name: "read_mcp_resource", Arguments: json.RawMessage(`{"server":"other","uri":"file:///secret"}`)}, dshDefinitions(nil))
	if err != nil || !result.IsError {
		t.Fatalf("accepted unknown resource server: %+v %v", result, err)
	}
}
