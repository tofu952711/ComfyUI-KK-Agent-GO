package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kkagent/internal/model"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (c *Client) readResponse(ctx context.Context, resp *http.Response, cfg model.ProviderConfig, delta model.Delta, started time.Time) (model.Turn, error) {
	p := Protocol(cfg)
	turn := model.Turn{Message: model.Message{Role: "assistant"}}
	first := func(s string) {
		if s == "" {
			return
		}
		if turn.FirstToken == 0 {
			turn.FirstToken = time.Since(started)
		}
		if delta != nil {
			delta(s)
		}
	}
	var complete map[string]any
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, e := io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes+1))
		if e != nil {
			return turn, e
		}
		if int64(len(b)) > c.MaxBytes {
			return turn, errors.New("provider response exceeds limit")
		}
		if e = json.Unmarshal(b, &complete); e != nil {
			return turn, errors.New("invalid provider JSON response")
		}
	} else {
		// A bounded SSE parser joins multi-line data and rejects incomplete streams.
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, c.MaxBytes+1))
		scanner.Buffer(make([]byte, 65536), int(c.MaxBytes))
		var data []string
		total := int64(0)
		finished := false
		chat := map[string]any{"role": "assistant"}
		calls := map[int]map[string]any{}
		var chatText strings.Builder
		blocks := map[int]map[string]any{}
		partial := map[int]*strings.Builder{}
		responseItems := map[int]any{}
		consume := func(raw string) error {
			if raw == "[DONE]" {
				finished = true
				return nil
			}
			var event map[string]any
			if e := json.Unmarshal([]byte(raw), &event); e != nil {
				return errors.New("invalid provider SSE JSON")
			}
			if _, ok := event["error"]; ok {
				return errors.New("provider stream reported an error")
			}
			switch p {
			case "chat":
				turn.Usage = mergeUsage(turn.Usage, event["usage"], p)
				for _, choice := range arr(event["choices"]) {
					ch := obj(choice)
					if num(ch["index"]) != 0 {
						continue
					}
					d := obj(ch["delta"])
					if content, ok := d["content"].(string); ok {
						chatText.WriteString(content)
						first(content)
					}
					for _, key := range []string{"reasoning_content", "reasoning", "refusal"} {
						if value, ok := d[key].(string); ok {
							chat[key] = str(chat[key]) + value
						}
					}
					for _, rawCall := range arr(d["tool_calls"]) {
						call := obj(rawCall)
						index := int(num(call["index"]))
						target := calls[index]
						if target == nil {
							target = map[string]any{"type": "function", "function": map[string]any{"name": "", "arguments": ""}}
							calls[index] = target
						}
						if id := str(call["id"]); id != "" {
							target["id"] = str(target["id"]) + id
						}
						fn := obj(call["function"])
						dest := obj(target["function"])
						dest["name"] = str(dest["name"]) + str(fn["name"])
						dest["arguments"] = str(dest["arguments"]) + str(fn["arguments"])
					}
					if reason := str(ch["finish_reason"]); reason != "" {
						turn.Finish = reason
					}
				}
			case "messages":
				switch str(event["type"]) {
				case "message_start":
					m := obj(event["message"])
					turn.Usage = mergeUsage(turn.Usage, m["usage"], p)
				case "content_block_start":
					index := int(num(event["index"]))
					b := obj(event["content_block"])
					blocks[index] = b
				case "content_block_delta":
					index := int(num(event["index"]))
					d := obj(event["delta"])
					b := blocks[index]
					if b == nil {
						return errors.New("content delta without block")
					}
					switch str(d["type"]) {
					case "text_delta":
						s := str(d["text"])
						b["text"] = str(b["text"]) + s
						first(s)
					case "input_json_delta":
						if partial[index] == nil {
							partial[index] = &strings.Builder{}
						}
						partial[index].WriteString(str(d["partial_json"]))
					case "thinking_delta":
						b["thinking"] = str(b["thinking"]) + str(d["thinking"])
					case "signature_delta":
						b["signature"] = str(b["signature"]) + str(d["signature"])
					default:
						return fmt.Errorf("unsupported messages delta %q", str(d["type"]))
					}
				case "message_delta":
					turn.Finish = str(obj(event["delta"])["stop_reason"])
					turn.Usage = mergeUsage(turn.Usage, event["usage"], p)
				case "message_stop":
					finished = true
				case "error":
					return errors.New("messages stream failed")
				}
			case "responses":
				switch str(event["type"]) {
				case "response.output_text.delta":
					first(str(event["delta"]))
				case "response.output_item.done":
					responseItems[int(num(event["output_index"]))] = event["item"]
				case "response.completed", "response.incomplete":
					complete = obj(event["response"])
					finished = true
				case "response.failed", "error":
					return errors.New("responses stream failed")
				}
			}
			return nil
		}
		flush := func() error {
			if len(data) == 0 {
				return nil
			}
			raw := strings.Join(data, "\n")
			data = nil
			return consume(raw)
		}
		for scanner.Scan() {
			if e := ctx.Err(); e != nil {
				return turn, e
			}
			line := scanner.Text()
			total += int64(len(line) + 1)
			if total > c.MaxBytes {
				return turn, errors.New("provider response exceeds byte limit")
			}
			if line == "" {
				if e := flush(); e != nil {
					return turn, e
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if e := scanner.Err(); e != nil {
			return turn, e
		}
		if e := flush(); e != nil {
			return turn, e
		}
		if !finished {
			return turn, errors.New("provider stream ended before completion")
		}
		switch p {
		case "chat":
			chat["content"] = chatText.String()
			indices := []int{}
			for index := range calls {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			if len(indices) > 0 {
				out := []any{}
				for _, index := range indices {
					out = append(out, calls[index])
				}
				chat["tool_calls"] = out
			}
			complete = map[string]any{"choices": []any{map[string]any{"message": chat, "finish_reason": turn.Finish}}}
		case "messages":
			indices := []int{}
			for index := range blocks {
				indices = append(indices, index)
			}
			sort.Ints(indices)
			content := []any{}
			for _, index := range indices {
				b := blocks[index]
				if part := partial[index]; part != nil {
					var input any
					if e := json.Unmarshal([]byte(part.String()), &input); e != nil {
						return turn, errors.New("invalid streamed tool arguments")
					}
					b["input"] = input
				}
				content = append(content, b)
			}
			complete = map[string]any{"content": content, "stop_reason": turn.Finish}
		case "responses":
			if complete == nil {
				return turn, errors.New("missing completed response")
			}
			if len(arr(complete["output"])) == 0 && len(responseItems) > 0 {
				indices := []int{}
				for i := range responseItems {
					indices = append(indices, i)
				}
				sort.Ints(indices)
				items := []any{}
				for _, i := range indices {
					items = append(items, responseItems[i])
				}
				complete["output"] = items
			}
		}
	}
	if complete == nil {
		return turn, errors.New("empty provider response")
	}
	turn.Usage = mergeUsage(turn.Usage, complete["usage"], p)
	switch p {
	case "chat":
		choices := arr(complete["choices"])
		if len(choices) == 0 {
			return turn, errors.New("provider returned no choices")
		}
		choice := obj(choices[0])
		message := obj(choice["message"])
		turn.Finish = str(choice["finish_reason"])
		if s := str(message["content"]); s != "" {
			turn.Message.Content = []model.Block{{Type: "text", Text: s}}
		}
		for _, item := range arr(message["tool_calls"]) {
			call := obj(item)
			fn := obj(call["function"])
			turn.Message.Calls = append(turn.Message.Calls, model.ToolCall{ID: str(call["id"]), Name: str(fn["name"]), Arguments: json.RawMessage(str(fn["arguments"]))})
		}
		turn.Message.Raw, _ = json.Marshal(message)
	case "messages":
		turn.Finish = str(complete["stop_reason"])
		for _, item := range arr(complete["content"]) {
			b := obj(item)
			if b["type"] == "text" {
				turn.Message.Content = append(turn.Message.Content, model.Block{Type: "text", Text: str(b["text"])})
			}
			if b["type"] == "tool_use" {
				arguments, _ := json.Marshal(b["input"])
				turn.Message.Calls = append(turn.Message.Calls, model.ToolCall{ID: str(b["id"]), Name: str(b["name"]), Arguments: arguments})
			}
		}
		turn.Message.Raw, _ = json.Marshal(map[string]any{"role": "assistant", "content": complete["content"]})
	case "responses":
		status := str(complete["status"])
		if status != "completed" {
			return turn, fmt.Errorf("response not completed: %s", status)
		}
		turn.Finish = "stop"
		for _, item := range arr(complete["output"]) {
			b := obj(item)
			if b["type"] == "function_call" {
				turn.Message.Calls = append(turn.Message.Calls, model.ToolCall{ID: str(b["call_id"]), Name: str(b["name"]), Arguments: json.RawMessage(str(b["arguments"]))})
			}
			if b["type"] == "message" {
				for _, content := range arr(b["content"]) {
					block := obj(content)
					if block["type"] == "output_text" {
						turn.Message.Content = append(turn.Message.Content, model.Block{Type: "text", Text: str(block["text"])})
					}
				}
			}
		}
		turn.Message.Raw, _ = json.Marshal(complete["output"])
	}
	if turn.Finish == "length" || turn.Finish == "max_tokens" {
		return turn, errors.New("model output limit reached")
	}
	for _, call := range turn.Message.Calls {
		if call.ID == "" || call.Name == "" || !json.Valid(call.Arguments) {
			return turn, errors.New("invalid model tool call")
		}
	}
	if len(turn.Message.Calls) > 0 {
		turn.Finish = "tool-calls"
	} else if turn.Finish == "stop" || turn.Finish == "end_turn" || turn.Finish == "stop_sequence" {
		turn.Finish = "completed"
	} else {
		return turn, fmt.Errorf("unsupported model finish reason %q", turn.Finish)
	}
	turn.Elapsed = time.Since(started)
	return turn, nil
}
