package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	HTTP     *http.Client
	Assets   *asset.Store
	MaxBytes int64
}

func New(a *asset.Store, max int64) *Client {
	return &Client{HTTP: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, Assets: a, MaxBytes: max}
}
func Protocol(c model.ProviderConfig) string {
	switch c.Protocol {
	case "openai-completions", "chat-completions":
		return "chat"
	case "deepseek-messages", "anthropic-messages":
		return "messages"
	case "codez", "openai-responses":
		u, _ := url.Parse(c.BaseURL)
		if u != nil && (u.Hostname() == "zdjapi.com" || strings.HasSuffix(u.Hostname(), ".zdjapi.com")) {
			return "chat"
		}
		return "responses"
	}
	return ""
}
func Endpoint(c model.ProviderConfig) (string, error) {
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid provider base URL")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("provider URL must not contain query or fragment")
	}
	p := Protocol(c)
	if p == "" {
		return "", errors.New("unsupported provider protocol")
	}
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		path = strings.TrimSuffix(path, suffix)
	}
	switch p {
	case "chat":
		path += "/chat/completions"
	case "responses":
		path += "/responses"
	case "messages":
		if !strings.HasSuffix(path, "/v1") {
			path += "/v1"
		}
		path += "/messages"
	}
	u.Path = path
	return u.String(), nil
}
func (c *Client) blocks(blocks []model.Block, kind string) ([]any, error) {
	out := []any{}
	for _, b := range blocks {
		if b.Type == "text" {
			t := "text"
			if kind == "responses" {
				t = "input_text"
			}
			out = append(out, map[string]any{"type": t, "text": b.Text})
			continue
		}
		data, e := c.Assets.Encode(b)
		if e != nil {
			return nil, e
		}
		uri := "data:" + b.MIME + ";base64," + data
		switch kind {
		case "chat":
			out = append(out, map[string]any{"type": "image_url", "image_url": map[string]any{"url": uri}})
		case "responses":
			out = append(out, map[string]any{"type": "input_image", "image_url": uri})
		case "messages":
			out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": b.MIME, "data": data}})
		}
	}
	return out, nil
}
func text(blocks []model.Block) string {
	var b strings.Builder
	for _, v := range blocks {
		if v.Type == "text" {
			b.WriteString(v.Text)
		}
	}
	return b.String()
}
func (c *Client) payload(r model.Request) (map[string]any, error) {
	p := Protocol(r.Config)
	body := map[string]any{"model": r.Config.Model, "stream": true}
	max := r.Config.MaxTokens
	if max < 1 {
		max = 32768
	}
	role := r.Config.InstructionRole
	if role == "" {
		role = "system"
	}
	if role != "system" && role != "developer" {
		return nil, errors.New("invalid instruction role")
	}
	messages := []any{}
	if p == "chat" && r.Rules != "" {
		messages = append(messages, map[string]any{"role": role, "content": r.Rules})
	}
	chatToolImages := []model.Block{}
	for messageIndex, m := range r.Messages {
		switch p {
		case "chat":
			if len(m.Raw) > 0 && m.Role == "assistant" {
				var raw map[string]any
				if e := json.Unmarshal(m.Raw, &raw); e != nil {
					return nil, e
				}
				if r.Config.WireCompatibility == "dsh-video" {
					if e := dshAssistantArguments(raw); e != nil {
						return nil, e
					}
				}
				messages = append(messages, raw)
				continue
			}
			if m.Role == "tool" {
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": m.CallID, "content": text(m.Content)})
				for _, b := range m.Content {
					if b.Type == "image" {
						chatToolImages = append(chatToolImages, b)
					}
				}
				if len(chatToolImages) > 0 && (messageIndex+1 == len(r.Messages) || r.Messages[messageIndex+1].Role != "tool") {
					blocks, e := c.blocks(chatToolImages, p)
					if e != nil {
						return nil, e
					}
					messages = append(messages, map[string]any{"role": "user", "content": blocks})
					chatToolImages = nil
				}
				continue
			}
			blocks, e := c.blocks(m.Content, p)
			if e != nil {
				return nil, e
			}
			messages = append(messages, map[string]any{"role": m.Role, "content": blocks})
		case "messages":
			if len(m.Raw) > 0 && m.Role == "assistant" {
				var raw any
				if e := json.Unmarshal(m.Raw, &raw); e != nil {
					return nil, e
				}
				messages = append(messages, raw)
				continue
			}
			blocks, e := c.blocks(m.Content, p)
			if e != nil {
				return nil, e
			}
			if m.Role == "tool" {
				blocks = []any{map[string]any{"type": "tool_result", "tool_use_id": m.CallID, "content": blocks, "is_error": m.IsError}}
			}
			mr := m.Role
			if mr == "tool" {
				mr = "user"
			}
			if len(messages) > 0 {
				last, ok := messages[len(messages)-1].(map[string]any)
				if ok && last["role"] == mr {
					if prev, ok := last["content"].([]any); ok {
						last["content"] = append(prev, blocks...)
						continue
					}
				}
			}
			messages = append(messages, map[string]any{"role": mr, "content": blocks})
		case "responses":
			if m.Role == "assistant" && len(m.Raw) > 0 {
				var items []any
				if e := json.Unmarshal(m.Raw, &items); e != nil {
					return nil, e
				}
				messages = append(messages, items...)
				continue
			}
			if m.Role == "tool" {
				blocks, e := c.blocks(m.Content, p)
				if e != nil {
					return nil, e
				}
				messages = append(messages, map[string]any{"type": "function_call_output", "call_id": m.CallID, "output": blocks})
				continue
			}
			blocks, e := c.blocks(m.Content, p)
			if e != nil {
				return nil, e
			}
			messages = append(messages, map[string]any{"role": m.Role, "content": blocks})
		}
	}
	definitions := []any{}
	for _, t := range r.Tools {
		var schema any
		if e := json.Unmarshal(t.Schema, &schema); e != nil {
			return nil, e
		}
		switch p {
		case "chat":
			definitions = append(definitions, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": schema}})
		case "messages":
			definitions = append(definitions, map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema})
		case "responses":
			definitions = append(definitions, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": schema})
		}
	}
	if len(definitions) > 0 {
		body["tools"] = definitions
	}
	switch p {
	case "chat":
		body["messages"] = messages
		body["max_tokens"] = max
		if r.Config.StreamUsage {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	case "messages":
		body["messages"] = messages
		body["system"] = r.Rules
		body["max_tokens"] = max
	case "responses":
		body["input"] = messages
		body["instructions"] = r.Rules
		body["max_output_tokens"] = max
		body["store"] = false
		body["include"] = []string{"reasoning.encrypted_content"}
	}
	if effort := r.Config.Reasoning; effort != "" && effort != "auto" {
		switch p {
		case "chat":
			body["reasoning_effort"] = effort
		case "responses":
			body["reasoning"] = map[string]any{"effort": effort}
		case "messages":
			body["output_config"] = map[string]any{"effort": effort}
		}
	}
	return body, nil
}

type HTTPError struct {
	Status       int
	RetryAfter   time.Duration
	RequestBytes int64
	ImageCount   int
}

func (e *HTTPError) Error() string {
	if e.Status == 413 {
		return fmt.Sprintf("provider HTTP 413：供应商拒绝了过大的请求体（%.2f MiB，%d张图）。请检查线路请求大小上限；任务没有自动重跑", float64(e.RequestBytes)/(1<<20), e.ImageCount)
	}
	return fmt.Sprintf("provider HTTP %d", e.Status)
}
func (c *Client) Next(ctx context.Context, r model.Request, delta model.Delta) (model.Turn, error) {
	endpoint, e := Endpoint(r.Config)
	if e != nil {
		return model.Turn{}, e
	}
	if r.Config.Model == "" {
		return model.Turn{}, errors.New("model is required")
	}
	raw, stats, e := c.requestBytes(ctx, r)
	if e != nil {
		return model.Turn{}, e
	}
	if int64(len(raw)) > c.MaxBytes {
		return model.Turn{}, errors.New("model request exceeds byte limit")
	}
	retries := r.Config.RetryCount
	if retries < 0 {
		retries = 0
	}
	if retries > 3 {
		retries = 3
	}
	for attempt := 0; ; attempt++ {
		timeout := r.Config.RequestTimeoutSeconds
		if timeout < 1 {
			timeout = 300
		}
		requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		req, e := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if e != nil {
			cancel()
			return model.Turn{}, e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if Protocol(r.Config) == "messages" {
			req.Header.Set("x-api-key", r.Config.APIKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+r.Config.APIKey)
		}
		started := time.Now()
		resp, e := c.HTTP.Do(req)
		retry := false
		delay := time.Duration(1<<attempt) * time.Second
		if e != nil {
			cancel()
			if ctx.Err() != nil {
				return model.Turn{}, ctx.Err()
			}
			return model.Turn{}, fmt.Errorf("provider transport failed: %w", e)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
			resp.Body.Close()
			if seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After")); seconds > 0 {
				delay = time.Duration(seconds) * time.Second
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			}
			e = &HTTPError{Status: resp.StatusCode, RetryAfter: delay, RequestBytes: int64(len(raw)), ImageCount: stats.ImageCount}
			retry = resp.StatusCode == 429 || resp.StatusCode == 408 || resp.StatusCode >= 500
			cancel()
		} else {
			turn, parseErr := c.readResponse(requestCtx, resp, r.Config, delta, started)
			resp.Body.Close()
			cancel()
			return turn, parseErr
		}
		if !retry || attempt >= retries {
			return model.Turn{}, e
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return model.Turn{}, ctx.Err()
		case <-timer.C:
		}
	}
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }
func str(v any) string         { s, _ := v.(string); return s }
func num(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}
func usage(v any, kind string) model.Usage {
	m := obj(v)
	if len(m) == 0 {
		return model.Usage{}
	}
	u := model.Usage{Known: true}
	switch kind {
	case "chat":
		u.Input = num(m["prompt_tokens"])
		u.Output = num(m["completion_tokens"])
		u.Cached = num(obj(m["prompt_tokens_details"])["cached_tokens"])
	case "messages":
		u.Cached = num(m["cache_read_input_tokens"])
		u.Input = num(m["input_tokens"]) + u.Cached + num(m["cache_creation_input_tokens"])
		u.Output = num(m["output_tokens"])
	default:
		u.Input = num(m["input_tokens"])
		u.Output = num(m["output_tokens"])
		u.Cached = num(obj(m["input_tokens_details"])["cached_tokens"])
	}
	u.Total = u.Input + u.Output
	return u
}
func mergeUsage(old model.Usage, v any, kind string) model.Usage {
	m := obj(v)
	if len(m) == 0 {
		return old
	}
	if kind != "messages" {
		return usage(v, kind)
	}
	next := usage(v, kind)
	if _, ok := m["input_tokens"]; !ok {
		next.Input = old.Input
		next.Cached = old.Cached
	}
	if _, ok := m["output_tokens"]; !ok {
		next.Output = old.Output
	}
	next.Total = next.Input + next.Output
	return next
}
