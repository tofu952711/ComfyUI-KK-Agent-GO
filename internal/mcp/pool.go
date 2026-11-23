package mcp

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"kkagent/internal/asset"
	"kkagent/internal/model"
	"kkagent/internal/tool"
	"sync"
	"time"
)

type entry struct {
	worker *worker
	busy   bool
	scope  model.Scope
	last   time.Time
}
type cached struct {
	key  string
	data []byte
}
type Pool struct {
	mu         sync.Mutex
	entries    map[string]*entry
	notify     chan struct{}
	closed     bool
	max        int
	maxBytes   int64
	assets     *asset.Store
	cache      *list.List
	cacheIndex map[string]*list.Element
	cacheSize  int64
	cacheLimit int64
}

func New(max int, maxBytes, cacheBytes int64, assets *asset.Store) *Pool {
	return &Pool{entries: map[string]*entry{}, notify: make(chan struct{}), max: max, maxBytes: maxBytes, assets: assets, cache: list.New(), cacheIndex: map[string]*list.Element{}, cacheLimit: cacheBytes}
}
func key(scope model.Scope) string {
	b, _ := json.Marshal(scope)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (p *Pool) signal() { close(p.notify); p.notify = make(chan struct{}) }
func (p *Pool) acquire(ctx context.Context, scope model.Scope) (*worker, func(bool), error) {
	k := key(scope)
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, nil, errors.New("tool pool closed")
		}
		if e := p.entries[k]; e != nil {
			if !e.busy {
				e.busy = true
				w := e.worker
				p.mu.Unlock()
				return w, p.releaseFunc(k, e), nil
			}
		} else {
			if len(p.entries) >= p.max {
				var oldestKey string
				var oldest *entry
				for candidate, e := range p.entries {
					if !e.busy && (oldest == nil || e.last.Before(oldest.last)) {
						oldest = e
						oldestKey = candidate
					}
				}
				if oldest != nil {
					delete(p.entries, oldestKey)
					oldest.worker.close()
				}
			}
			if len(p.entries) < p.max {
				e := &entry{busy: true, scope: scope, last: time.Now()}
				p.entries[k] = e
				p.mu.Unlock()
				w, err := start(ctx, scope, p.maxBytes)
				p.mu.Lock()
				if err != nil || p.closed {
					delete(p.entries, k)
					p.signal()
					p.mu.Unlock()
					if w != nil {
						w.close()
					}
					if err == nil {
						err = errors.New("pool closed")
					}
					return nil, nil, err
				}
				e.worker = w
				p.mu.Unlock()
				return w, p.releaseFunc(k, e), nil
			}
		}
		signal := p.notify
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-signal:
		}
	}
}
func (p *Pool) releaseFunc(k string, e *entry) func(bool) {
	return func(broken bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.entries[k] != e {
			return
		}
		if broken {
			delete(p.entries, k)
			e.worker.close()
		} else {
			e.busy = false
			e.last = time.Now()
		}
		p.signal()
	}
}
func (p *Pool) List(ctx context.Context, scope model.Scope) ([]model.ToolDefinition, error) {
	w, release, e := p.acquire(ctx, scope)
	if e != nil {
		return nil, e
	}
	defer release(false)
	return append([]model.ToolDefinition(nil), w.definitions...), nil
}

// Resource uses the same scoped worker and lifetime as media tool calls.
func (p *Pool) Resource(ctx context.Context, scope model.Scope, method string, args map[string]any) (json.RawMessage, error) {
	if method != "resources/list" && method != "resources/templates/list" && method != "resources/read" {
		return nil, errors.New("unsupported MCP resource method")
	}
	w, release, err := p.acquire(ctx, scope)
	if err != nil {
		return nil, err
	}
	if !w.resources && method != "resources/read" {
		release(false)
		if method == "resources/list" {
			return json.RawMessage(`{"resources":[]}`), nil
		}
		return json.RawMessage(`{"resourceTemplates":[]}`), nil
	}
	raw, callErr := w.rpc(ctx, method, args)
	var protocol *protocolError
	isProtocolError := errors.As(callErr, &protocol)
	release(callErr != nil && !isProtocolError)
	if isProtocolError {
		return nil, errors.New(protocol.message)
	}
	return raw, callErr
}
func (p *Pool) Execute(ctx context.Context, scope model.Scope, call model.ToolCall) (model.ToolResult, error) {
	w, release, e := p.acquire(ctx, scope)
	if e != nil {
		return model.ToolResult{}, e
	}
	broken := false
	defer func() { release(broken) }()
	found := false
	for _, d := range w.definitions {
		if d.Name == call.Name {
			found = true
			if e := tool.Validate(d.Schema, call.Arguments); e != nil {
				return model.ToolResult{Content: []model.Block{{Type: "text", Text: e.Error()}}, IsError: true}, nil
			}
			break
		}
	}
	if !found {
		return model.ToolResult{}, errors.New("tool not registered in this scope")
	}
	// B workers also enforce their materialized segment scope. Check get_frames locally.
	if scope.End > scope.Start && call.Name == "get_frames" {
		var args struct {
			Times []float64 `json:"times"`
		}
		_ = json.Unmarshal(call.Arguments, &args)
		for _, t := range args.Times {
			if t < scope.Start || t > scope.End {
				return model.ToolResult{Content: []model.Block{{Type: "text", Text: "requested frame outside task range"}}, IsError: true}, nil
			}
		}
	}
	if call.Name == "set_video_segments" && scope.Phase != "segment" {
		return model.ToolResult{}, errors.New("segmentation tool unavailable in this phase")
	}
	var arguments any
	_ = json.Unmarshal(call.Arguments, &arguments)
	cacheKey := key(scope) + ":" + call.Name + ":" + string(call.Arguments)
	readonly := call.Name != "set_video_segments"
	if readonly {
		if hit, ok := p.getCache(cacheKey); ok {
			return hit, nil
		}
	}
	ms := scope.MCP.CallMS
	if ms < 1 {
		ms = 180000
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	defer cancel()
	raw, e := w.rpc(callCtx, "tools/call", map[string]any{"name": call.Name, "arguments": arguments})
	if e != nil {
		broken = true
		return model.ToolResult{}, e
	}
	var result model.ToolResult
	if e = json.Unmarshal(raw, &result); e != nil {
		broken = true
		return result, errors.New("invalid MCP tool result")
	}
	result.Content, e = p.assets.Spool(result.Content)
	if e != nil {
		return model.ToolResult{}, e
	}
	if readonly && !result.IsError {
		p.putCache(cacheKey, result)
	}
	return result, nil
}
func (p *Pool) getCache(k string) (model.ToolResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	element := p.cacheIndex[k]
	if element == nil {
		return model.ToolResult{}, false
	}
	p.cache.MoveToFront(element)
	var result model.ToolResult
	if json.Unmarshal(element.Value.(cached).data, &result) != nil {
		return result, false
	}
	return result, true
}
func (p *Pool) putCache(k string, value model.ToolResult) {
	raw, _ := json.Marshal(value)
	p.mu.Lock()
	defer p.mu.Unlock()
	if int64(len(raw)) > p.cacheLimit || p.cacheLimit == 0 {
		return
	}
	if old := p.cacheIndex[k]; old != nil {
		p.cacheSize -= int64(len(old.Value.(cached).data))
		p.cache.Remove(old)
	}
	element := p.cache.PushFront(cached{k, raw})
	p.cacheIndex[k] = element
	p.cacheSize += int64(len(raw))
	for p.cacheSize > p.cacheLimit {
		last := p.cache.Back()
		v := last.Value.(cached)
		delete(p.cacheIndex, v.key)
		p.cacheSize -= int64(len(v.data))
		p.cache.Remove(last)
	}
}
func (p *Pool) Release(scope model.Scope) {
	k := key(scope)
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[k]; e != nil && !e.busy {
		delete(p.entries, k)
		e.worker.close()
		p.signal()
	}
}
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	workers := []*worker{}
	for _, e := range p.entries {
		if e.worker != nil {
			workers = append(workers, e.worker)
		}
	}
	p.entries = map[string]*entry{}
	p.signal()
	p.mu.Unlock()
	for _, w := range workers {
		w.close()
	}
	return nil
}
