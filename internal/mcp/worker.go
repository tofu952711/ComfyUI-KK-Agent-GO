package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"kkagent/internal/model"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
type protocolError struct {
	code    int
	message string
}

func (e *protocolError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.code, e.message) }

type worker struct {
	cmd         *exec.Cmd
	input       io.WriteCloser
	lines       chan []byte
	done        chan struct{}
	wait        chan struct{}
	once        sync.Once
	mu          sync.Mutex
	sequence    uint64
	max         int64
	definitions []model.ToolDefinition
	resources   bool
}

func start(ctx context.Context, scope model.Scope, max int64) (*worker, error) {
	cfg := scope.MCP
	if cfg.Command == "" {
		return nil, errors.New("missing MCP command")
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	configure(cmd)
	cmd.Dir = cfg.Cwd
	// Media workers inherit runtime/library settings but never model credentials.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") || upper == "NODE_OPTIONS" {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	for name, value := range cfg.Env {
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "API_KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") {
			return nil, errors.New("MCP environment cannot contain credentials")
		}
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		return nil, errors.New("failed to start MCP executable")
	}
	w := &worker{cmd: cmd, input: stdin, lines: make(chan []byte, 4), done: make(chan struct{}), wait: make(chan struct{}), max: max}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 65536), int(max))
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case w.lines <- line:
			case <-w.done:
				return
			}
		}
		close(w.lines)
	}()
	go func() { _ = cmd.Wait(); close(w.wait) }()
	ms := cfg.StartupMS
	if ms < 1 {
		ms = 30000
	}
	initCtx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	defer cancel()
	initialized, e := w.rpc(initCtx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "KKagent-go", "version": "0.1.3"}})
	if e != nil {
		w.close()
		return nil, e
	}
	var capabilities struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(initialized, &capabilities) == nil {
		_, w.resources = capabilities.Capabilities["resources"]
	}
	if e = w.notify("notifications/initialized", map[string]any{}); e != nil {
		w.close()
		return nil, e
	}
	result, e := w.rpc(initCtx, "tools/list", map[string]any{})
	if e != nil {
		w.close()
		return nil, e
	}
	var listing struct {
		Tools []model.ToolDefinition `json:"tools"`
	}
	if e = json.Unmarshal(result, &listing); e != nil {
		w.close()
		return nil, errors.New("invalid MCP tools listing")
	}
	w.definitions = listing.Tools
	return w, nil
}
func (w *worker) notify(method string, params any) error {
	b, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if e != nil {
		return e
	}
	_, e = w.input.Write(append(b, '\n'))
	return e
}
func (w *worker) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sequence++
	id := w.sequence
	raw, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) > w.max {
		return nil, errors.New("MCP request exceeds byte limit")
	}
	// Tool pipes can block on native workers; close terminates writes on cancellation.
	sent := make(chan error, 1)
	go func() { _, e := w.input.Write(append(raw, '\n')); sent <- e }()
	select {
	case <-ctx.Done():
		w.close()
		return nil, ctx.Err()
	case e := <-sent:
		if e != nil {
			return nil, errors.New("MCP input pipe closed")
		}
	}
	for {
		select {
		case <-ctx.Done():
			w.close()
			return nil, ctx.Err()
		case <-w.done:
			return nil, errors.New("MCP worker stopped")
		case line, ok := <-w.lines:
			if !ok {
				return nil, errors.New("MCP worker exited or exceeded message limit")
			}
			var response rpcResponse
			if e = json.Unmarshal(line, &response); e != nil {
				return nil, errors.New("invalid MCP JSON response")
			}
			if response.ID == 0 {
				continue
			}
			if response.ID != id {
				return nil, errors.New("unexpected MCP response ID")
			}
			if response.Error != nil {
				return nil, &protocolError{code: response.Error.Code, message: response.Error.Message}
			}
			return response.Result, nil
		}
	}
}
func (w *worker) close() {
	w.once.Do(func() {
		close(w.done)
		_ = w.input.Close()
		kill(w.cmd)
		select {
		case <-w.wait:
		case <-time.After(3 * time.Second):
		}
	})
}
