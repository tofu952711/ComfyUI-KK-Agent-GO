package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"kkagent/internal/model"
	"kkagent/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (e *Engine) loop(r *record, spec model.Spec, sessionKey string) (model.Result, error) {
	ctx := r.ctx
	result := model.Result{Usage: model.Usage{Known: true}, TokenSpeed: []model.TokenRecord{}}
	messages := []model.Message{}
	historyPath := filepath.Join(e.cfg.DataDir, "sessions", sessionKey+".json")
	if raw, err := os.ReadFile(historyPath); err == nil {
		if int64(len(raw)) > e.cfg.MaxResponseBytes {
			return result, errors.New("session history exceeds byte limit")
		}
		if err = json.Unmarshal(raw, &messages); err != nil {
			return result, errors.New("corrupt session history")
		}
	}
	dshCompatible := spec.Provider.WireCompatibility == "dsh-video"
	content := []model.Block{}
	if spec.Task != "" || dshCompatible {
		content = append(content, model.Block{Type: "text", Text: spec.Task})
	}
	if spec.Evidence != "" {
		content = append(content, model.Block{Type: "text", Text: spec.Evidence})
	}
	content = append(content, spec.Blocks...)
	messages = append(messages, model.Message{Role: "user", Content: content})
	definitions := []model.ToolDefinition{}
	if spec.ToolsEnabled {
		e.state(r, "waiting_tool")
		if spec.Scope == nil {
			return result, errors.New("tool scope required")
		}
		defer e.tools.Release(*spec.Scope)
		var err error
		definitions, err = e.tools.List(ctx, *spec.Scope)
		if err != nil {
			return result, err
		}
		if len(spec.AllowedTools) > 0 {
			allowed := map[string]bool{}
			for _, name := range spec.AllowedTools {
				allowed[name] = true
			}
			filtered := []model.ToolDefinition{}
			for _, d := range definitions {
				if allowed[d.Name] {
					filtered = append(filtered, d)
				}
			}
			definitions = filtered
		}
		if spec.Scope.Phase != "segment" {
			filtered := []model.ToolDefinition{}
			for _, d := range definitions {
				if d.Name != "set_video_segments" {
					filtered = append(filtered, d)
				}
			}
			definitions = filtered
		}
		if dshCompatible {
			definitions = dshDefinitions(definitions)
		}
	}
	allowed := map[string]bool{}
	for _, d := range definitions {
		allowed[d.Name] = true
	}
	completed := map[string]struct {
		signature string
		message   model.Message
	}{}
	toolCount := 0
	for round := 0; round < spec.Limits.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		e.state(r, "queued")
		if err := acquire(ctx, e.active); err != nil {
			return result, err
		}
		e.state(r, "running")
		if err := acquire(ctx, e.llm); err != nil {
			<-e.active
			return result, err
		}
		requestID := id()
		e.emit(r, "step/start", map[string]any{"round": round + 1, "request_id": requestID})
		turn, err := e.provider.Next(ctx, model.Request{Config: *spec.Provider, Rules: spec.Rules, Messages: messages, Tools: definitions}, nil)
		<-e.llm
		<-e.active
		if err != nil {
			return result, err
		}
		result.Usage.Add(turn.Usage)
		var tokens *int64
		var speed *float64
		if turn.Usage.Known {
			n := turn.Usage.Output
			tokens = &n
			if turn.Elapsed.Seconds() > 0 {
				value := float64(n) / turn.Elapsed.Seconds()
				speed = &value
			}
		}
		record := model.TokenRecord{Kind: "end", RequestID: requestID, Model: spec.Provider.Model, Elapsed: turn.Elapsed.Seconds(), FirstToken: turn.FirstToken.Seconds(), Output: tokens, TPS: speed, Finish: turn.Finish, Usage: turn.Usage}
		result.TokenSpeed = append(result.TokenSpeed, record)
		e.emit(r, "llm/usage", map[string]any{"record": record})
		messages = append(messages, turn.Message)
		e.emit(r, "assistant/message", map[string]any{"usage": turn.Usage, "tool_count": len(turn.Message.Calls), "text_characters": len(joinText(turn.Message.Content))})
		if len(turn.Message.Calls) == 0 {
			if turn.Finish != "completed" {
				return result, errors.New("model did not complete normally")
			}
			body := joinText(turn.Message.Content)
			if strings.TrimSpace(body) == "" {
				return result, errors.New("model returned empty final response")
			}
			result.Text = body
			result.Finish = "completed"
			if err := store.Atomic(historyPath, messages); err != nil {
				return result, err
			}
			return result, nil
		}
		if len(turn.Message.Calls) > spec.Limits.MaxTools-toolCount {
			return result, errors.New("tool call limit reached")
		}
		for _, call := range turn.Message.Calls {
			toolCount++
			if !allowed[call.Name] {
				return result, fmt.Errorf("model requested unavailable tool %q", call.Name)
			}
			signature := hash([]any{call.Name, string(call.Arguments)})
			if previous, ok := completed[call.ID]; ok {
				if previous.signature != signature {
					return result, errors.New("tool call ID reused with different arguments")
				}
				messages = append(messages, previous.message)
				continue
			}
			e.state(r, "waiting_tool")
			e.emit(r, "tool/call", map[string]any{"id": call.ID, "name": call.Name, "arguments": json.RawMessage(call.Arguments)})
			toolCtx, cancel := context.WithTimeout(ctx, time.Duration(spec.Limits.ToolTimeoutSeconds)*time.Second)
			began := time.Now()
			var output model.ToolResult
			var err error
			if dshCompatible {
				output, err = e.dshTool(toolCtx, *spec.Scope, call, definitions)
			} else {
				output, err = e.tools.Execute(toolCtx, *spec.Scope, call)
			}
			cancel()
			if err != nil {
				return result, fmt.Errorf("tool %s failed: %w", call.Name, err)
			}
			spooled, err := e.assets.Spool(output.Content)
			if err != nil {
				return result, err
			}
			if dshCompatible {
				spooled, err = e.dshToolBlocks(spooled)
				if err != nil {
					return result, err
				}
			}
			message := model.Message{Role: "tool", CallID: call.ID, Content: spooled, IsError: output.IsError}
			completed[call.ID] = struct {
				signature string
				message   model.Message
			}{signature, message}
			messages = append(messages, message)
			e.emit(r, "tool/result", map[string]any{"id": call.ID, "name": call.Name, "is_error": output.IsError, "content_blocks": len(spooled), "seconds": time.Since(began).Seconds()})
		}
		if err := store.Atomic(historyPath, messages); err != nil {
			return result, err
		}
	}
	return result, errors.New("model round limit reached")
}
func joinText(blocks []model.Block) string {
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}
