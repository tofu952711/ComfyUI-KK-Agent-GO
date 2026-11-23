package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func event(w http.ResponseWriter, v any) { b, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", b) }
func TestChatFragmentedToolsReasoningAndImageReplay(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 1<<20)
	blocks, err := a.Spool([]model.Block{{Type: "image", MIME: "image/png", Data: "aW1hZ2U="}})
	if err != nil {
		t.Fatal(err)
	}
	var requests []map[string]any
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, body)
		n := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			event(w, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": "preserve me", "tool_calls": []any{map[string]any{"index": 0, "id": "call-1", "function": map[string]any{"name": "get_frames", "arguments": "{\"times\":"}}}}}}})
			event(w, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": "[1]}"}}}}, "finish_reason": "tool_calls"}}})
		} else {
			event(w, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "done"}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 2}})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	client := New(a, 1<<20)
	cfg := model.ProviderConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", Model: "test"}
	initial := model.Message{Role: "user", Content: []model.Block{{Type: "text", Text: "look"}}}
	first, err := client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Message.Calls) != 1 || string(first.Message.Calls[0].Arguments) != "{\"times\":[1]}" {
		t.Fatalf("bad assembly: %+v", first.Message)
	}
	second, err := client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial, first.Message, {Role: "tool", CallID: "call-1", Content: append([]model.Block{{Type: "text", Text: "frame"}}, blocks...)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Finish != "completed" || second.Usage.Total != 5 {
		t.Fatalf("unexpected final: %+v", second)
	}
	encoded, _ := json.Marshal(requests[1])
	if !strings.Contains(string(encoded), "preserve me") || !strings.Contains(string(encoded), "image_url") {
		t.Fatal("lost reasoning or tool image on continuation")
	}
}
func TestMessagesThinkingSignatureAndToolReplay(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 1<<20)
	var body map[string]any
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event(w, map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 10}}})
		if count == 1 {
			event(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}})
			event(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "reason"}})
			event(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "signed"}})
			event(w, map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "tool1", "name": "inspect_video", "input": map[string]any{}}})
			event(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 2}})
		} else {
			event(w, map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			event(w, map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "ok"}})
			event(w, map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 1}})
		}
		event(w, map[string]any{"type": "message_stop"})
	}))
	defer server.Close()
	client := New(a, 1<<20)
	cfg := model.ProviderConfig{Protocol: "deepseek-messages", BaseURL: server.URL + "/anthropic", Model: "test"}
	initial := model.Message{Role: "user", Content: []model.Block{{Type: "text", Text: "inspect"}}}
	first, err := client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Usage.Input != 10 || first.Usage.Output != 2 {
		t.Fatalf("usage merging failed: %+v", first.Usage)
	}
	_, err = client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial, first.Message, {Role: "tool", CallID: "tool1", Content: []model.Block{{Type: "text", Text: "result"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "signed") || !strings.Contains(string(raw), "tool_result") {
		t.Fatal("lost signed reasoning or tool result")
	}
}
func TestResponsesReplaysReasoningAndRejectsIncomplete(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 1<<20)
	var body map[string]any
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			event(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "reasoning", "id": "rs1", "encrypted_content": "encrypted"}, map[string]any{"type": "function_call", "call_id": "fc1", "name": "get_frames", "arguments": "{}"}}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 2}}})
		} else if count == 2 {
			event(w, map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}}}})
		} else {
			event(w, map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete", "output": []any{}}})
		}
	}))
	defer server.Close()
	client := New(a, 1<<20)
	cfg := model.ProviderConfig{Protocol: "codez", BaseURL: server.URL + "/v1", Model: "test"}
	initial := model.Message{Role: "user", Content: []model.Block{{Type: "text", Text: "hi"}}}
	first, err := client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial, first.Message, {Role: "tool", CallID: "fc1", Content: []model.Block{{Type: "text", Text: "image result"}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "encrypted") || !strings.Contains(string(raw), "function_call_output") {
		t.Fatal("lost response replay items")
	}
	if _, err = client.Next(context.Background(), model.Request{Config: cfg, Messages: []model.Message{initial}}, nil); err == nil {
		t.Fatal("accepted incomplete response")
	}
}
func TestTruncatedStreamDoesNotReplayRequest(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 1<<20)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		event(w, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "partial"}}}})
	}))
	defer server.Close()
	client := New(a, 1<<20)
	_, err := client.Next(context.Background(), model.Request{Config: model.ProviderConfig{Protocol: "openai-completions", BaseURL: server.URL, Model: "test", RetryCount: 3}}, nil)
	if err == nil || calls != 1 {
		t.Fatalf("unsafe partial-stream retry: calls=%d err=%v", calls, err)
	}
}

func TestChatAllToolResultsPrecedeToolImages(t *testing.T) {
	a, _ := asset.New(t.TempDir(), 1<<20)
	blocks, _ := a.Spool([]model.Block{{Type: "image", Data: "aW1hZ2U=", MIME: "image/png"}})
	c := New(a, 1<<20)
	request := model.Request{Config: model.ProviderConfig{Protocol: "openai-completions"}, Messages: []model.Message{{Role: "tool", CallID: "first", Content: blocks}, {Role: "tool", CallID: "second", Content: []model.Block{{Type: "text", Text: "second result"}}}}}
	payload, err := c.payload(request)
	if err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 || obj(messages[0])["role"] != "tool" || obj(messages[1])["role"] != "tool" || obj(messages[2])["role"] != "user" {
		t.Fatal("images interrupted the contiguous tool result sequence")
	}
}
