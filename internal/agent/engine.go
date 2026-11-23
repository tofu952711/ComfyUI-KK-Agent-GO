package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"kkagent/internal/asset"
	"kkagent/internal/config"
	"kkagent/internal/model"
	"kkagent/internal/provider"
	"kkagent/internal/store"
	"os"
	"strings"
	"sync"
	"time"
)

type record struct {
	mu       sync.Mutex
	snapshot model.Snapshot
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	seq      int64
}
type sessionLock struct {
	token chan struct{}
	refs  int
}
type Engine struct {
	cfg          config.Config
	provider     model.Provider
	tools        model.Tools
	assets       *asset.Store
	disk         *store.Store
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	runs         map[string]*record
	clients      map[string]string
	sessionLocks map[string]*sessionLock
	active       chan struct{}
	llm          chan struct{}
	wg           sync.WaitGroup
	closed       bool
}

func New(cfg config.Config, p model.Provider, t model.Tools, a *asset.Store, d *store.Store) (*Engine, error) {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{cfg: cfg, provider: p, tools: t, assets: a, disk: d, ctx: ctx, cancel: cancel, runs: map[string]*record{}, clients: map[string]string{}, sessionLocks: map[string]*sessionLock{}, active: make(chan struct{}, cfg.MaxActive), llm: make(chan struct{}, cfg.MaxLLM)}
	err := d.Recover()
	if err != nil {
		cancel()
		return nil, err
	}
	return e, nil
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (e *Engine) resolve(spec model.Spec) (model.Spec, error) {
	if spec.ClientID == "" || len(spec.ClientID) > 256 {
		return spec, errors.New("client_request_id is required (max 256 bytes)")
	}
	if spec.SessionID == "" {
		spec.SessionID = spec.ClientID
	}
	if len(spec.SessionID) > 256 {
		return spec, errors.New("session_id too long")
	}
	if len(spec.SessionNamespace) > 256 {
		return spec, errors.New("session_namespace too long")
	}
	if spec.Kind == "batch" {
		if spec.Limits.TimeoutSeconds > 7200 {
			return spec, errors.New("timeout exceeds 7200 seconds")
		}
		if len(spec.Children) < 1 || len(spec.Children) > 32 {
			return spec, errors.New("batch requires 1..32 children")
		}
		return spec, nil
	}
	if spec.Kind != "" && spec.Kind != "agent" {
		return spec, errors.New("unsupported run kind")
	}
	if spec.Provider == nil {
		p, ok := e.cfg.Providers[spec.ProviderID]
		if !ok {
			return spec, errors.New("provider configuration not found")
		}
		spec.Provider = &p
	} else {
		p := *spec.Provider
		spec.Provider = &p
	}
	if spec.Provider.APIKey == "" && spec.Provider.APIKeyEnv != "" {
		spec.Provider.APIKey = os.Getenv(spec.Provider.APIKeyEnv)
	}
	if _, err := provider.Endpoint(*spec.Provider); err != nil {
		return spec, err
	}
	if mode := spec.Provider.WireCompatibility; mode != "" && (mode != "dsh-video" || provider.Protocol(*spec.Provider) != "chat") {
		return spec, errors.New("wire_compatibility dsh-video requires chat completions")
	}
	if spec.Provider.Model == "" {
		return spec, errors.New("provider model required")
	}
	if spec.Task == "" && spec.Evidence == "" && len(spec.Blocks) == 0 && len(spec.AssetRefs) == 0 {
		return spec, errors.New("task, evidence or content required")
	}
	if spec.Scope == nil && spec.ScopeID != "" {
		scope, ok := e.cfg.Scopes[spec.ScopeID]
		if !ok {
			return spec, errors.New("tool scope not found")
		}
		spec.Scope = &scope
	}
	if spec.Scope != nil {
		scope := *spec.Scope
		spec.Scope = &scope
		if scope.ID == "" {
			scope.ID = spec.SessionID
		}
		if scope.Phase == "" {
			scope.Phase = "reverse"
		}
	}
	if spec.ToolsEnabled && spec.Scope == nil {
		return spec, errors.New("tools enabled without tool scope")
	}
	for _, ref := range spec.AssetRefs {
		assetID := ref
		if path, ok := e.cfg.Assets[ref]; ok {
			var err error
			assetID, err = e.assets.Import(path)
			if err != nil {
				return spec, fmt.Errorf("cannot import configured asset")
			}
		}
		spec.Blocks = append(spec.Blocks, model.Block{Type: "image", Asset: assetID, MIME: "image/png"})
	}
	spec.AssetRefs = nil
	blocks, err := e.assets.Spool(spec.Blocks)
	if err != nil {
		return spec, err
	}
	spec.Blocks = blocks
	if spec.Limits.TimeoutSeconds < 1 {
		spec.Limits.TimeoutSeconds = 900
	}
	if spec.Limits.TimeoutSeconds > 7200 {
		return spec, errors.New("timeout exceeds 7200 seconds")
	}
	if spec.Limits.MaxRounds < 1 {
		spec.Limits.MaxRounds = 20
	}
	if spec.Limits.MaxRounds > 200 {
		return spec, errors.New("model round limit exceeds 200")
	}
	if spec.Limits.MaxTools < 1 {
		spec.Limits.MaxTools = 40
	}
	if spec.Limits.MaxTools > 1000 {
		return spec, errors.New("tool limit exceeds 1000")
	}
	if spec.Limits.ToolTimeoutSeconds < 1 {
		spec.Limits.ToolTimeoutSeconds = 180
	}
	return spec, nil
}
func (e *Engine) Start(spec model.Spec) (model.Snapshot, error) {
	if spec.Kind == "batch" && spec.Limits.TimeoutSeconds < 1 {
		spec.Limits.TimeoutSeconds = 900
	}
	var err error
	spec, err = e.resolve(spec)
	if err != nil {
		return model.Snapshot{}, err
	}
	fingerprint := hash(spec)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return model.Snapshot{}, errors.New("service shutting down")
	}
	existing := e.clients[spec.ClientID]
	if existing == "" {
		existing, _ = e.disk.Client(spec.ClientID)
	}
	if existing != "" {
		var snapshot model.Snapshot
		if r := e.runs[existing]; r != nil {
			r.mu.Lock()
			snapshot = r.snapshot
			r.mu.Unlock()
		} else {
			snapshot, err = e.disk.Load(existing)
		}
		if err != nil {
			return snapshot, err
		}
		if snapshot.SpecHash != fingerprint {
			return snapshot, errors.New("request ID already used with different input")
		}
		return clone(snapshot), nil
	}
	pending := 0
	for _, r := range e.runs {
		r.mu.Lock()
		if !model.Terminal(r.snapshot.Status) {
			pending++
		}
		r.mu.Unlock()
	}
	if pending >= e.cfg.MaxQueued {
		return model.Snapshot{}, errors.New("run queue is full")
	}
	parentCtx := e.ctx
	var parent *record
	if spec.ParentID != "" {
		depth := 0
		ancestor := spec.ParentID
		for ancestor != "" {
			p := e.runs[ancestor]
			if p == nil {
				break
			}
			p.mu.Lock()
			ancestor = p.snapshot.ParentID
			p.mu.Unlock()
			depth++
			if depth >= 8 {
				return model.Snapshot{}, errors.New("maximum child depth reached")
			}
		}
		parent = e.runs[spec.ParentID]
		if parent == nil {
			return model.Snapshot{}, errors.New("parent run must be active")
		}
		parent.mu.Lock()
		terminal := model.Terminal(parent.snapshot.Status)
		parent.mu.Unlock()
		if terminal || parent.ctx.Err() != nil {
			return model.Snapshot{}, errors.New("parent run already ended")
		}
		parentCtx = parent.ctx
	}
	ctx, cancel := context.WithTimeout(parentCtx, time.Duration(spec.Limits.TimeoutSeconds)*time.Second)
	now := time.Now().UTC()
	r := &record{ctx: ctx, cancel: cancel, done: make(chan struct{}), snapshot: model.Snapshot{ID: id(), ClientID: spec.ClientID, SessionID: spec.SessionID, ParentID: spec.ParentID, Status: "queued", Created: now, Updated: now, SpecHash: fingerprint}}
	if err = e.disk.Save(r.snapshot); err != nil {
		cancel()
		return model.Snapshot{}, err
	}
	if err = e.disk.SaveClient(spec.ClientID, r.snapshot.ID); err != nil {
		cancel()
		return model.Snapshot{}, err
	}
	e.runs[r.snapshot.ID] = r
	e.clients[spec.ClientID] = r.snapshot.ID
	if parent != nil {
		parent.mu.Lock()
		parent.snapshot.Children = append(parent.snapshot.Children, r.snapshot.ID)
		_ = e.disk.Save(parent.snapshot)
		parent.mu.Unlock()
	}
	e.pruneLocked()
	initial := clone(r.snapshot)
	e.wg.Add(1)
	go func() { defer e.wg.Done(); e.run(r, spec) }()
	return initial, nil
}
func clone(s model.Snapshot) model.Snapshot {
	b, _ := json.Marshal(s)
	var copy model.Snapshot
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (e *Engine) pruneLocked() {
	for len(e.runs) > e.cfg.MaxRetained {
		var oldestID string
		var oldest time.Time
		for id, r := range e.runs {
			r.mu.Lock()
			terminal := model.Terminal(r.snapshot.Status)
			updated := r.snapshot.Updated
			r.mu.Unlock()
			if terminal && (oldestID == "" || updated.Before(oldest)) {
				oldestID = id
				oldest = updated
			}
		}
		if oldestID == "" {
			break
		}
		delete(e.clients, e.runs[oldestID].snapshot.ClientID)
		delete(e.runs, oldestID)
	}
}
func (e *Engine) Get(id string) (model.Snapshot, error) {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r == nil {
		return e.disk.Load(id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return clone(r.snapshot), nil
}
func (e *Engine) Events(id string, after int64) ([]model.Event, error) {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
	} else if _, err := e.disk.Load(id); err != nil {
		return nil, err
	}
	return e.disk.Events(id, after, 256)
}
func (e *Engine) Wait(ctx context.Context, id string) (model.Snapshot, error) {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r == nil {
		return e.disk.Load(id)
	}
	select {
	case <-ctx.Done():
		return model.Snapshot{}, ctx.Err()
	case <-r.done:
		return e.Get(id)
	}
}
func (e *Engine) Cancel(id string) error {
	e.mu.Lock()
	r := e.runs[id]
	e.mu.Unlock()
	if r == nil {
		_, err := e.disk.Load(id)
		return err
	}
	r.cancel()
	return nil
}
func (e *Engine) emit(r *record, kind string, data map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	event := model.Event{Seq: r.seq, Type: kind, Time: time.Now().UTC(), Data: data}
	if err := e.disk.Append(r.snapshot.ID, event); err != nil {
		r.cancel()
		return
	}
	r.snapshot.LastSeq = r.seq
	r.snapshot.Updated = event.Time
	_ = e.disk.Save(r.snapshot)
}
func (e *Engine) state(r *record, state string) {
	r.mu.Lock()
	if !model.Terminal(r.snapshot.Status) {
		r.snapshot.Status = state
		r.snapshot.Updated = time.Now().UTC()
		_ = e.disk.Save(r.snapshot)
	}
	r.mu.Unlock()
}
func acquire(ctx context.Context, sem chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case sem <- struct{}{}:
		return nil
	}
}
func (e *Engine) lockSession(ctx context.Context, key string) (func(), error) {
	e.mu.Lock()
	s := e.sessionLocks[key]
	if s == nil {
		s = &sessionLock{token: make(chan struct{}, 1)}
		e.sessionLocks[key] = s
	}
	s.refs++
	e.mu.Unlock()
	cleanup := func(acquired bool) {
		if acquired {
			<-s.token
		}
		e.mu.Lock()
		s.refs--
		if s.refs == 0 {
			delete(e.sessionLocks, key)
		}
		e.mu.Unlock()
	}
	if err := acquire(ctx, s.token); err != nil {
		cleanup(false)
		return nil, err
	}
	return func() { cleanup(true) }, nil
}
func (e *Engine) run(r *record, spec model.Spec) {
	var result model.Result
	var err error
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.New("internal run failure")
		}
		status := "completed"
		if err != nil {
			status = "failed"
		}
		if r.ctx.Err() == context.Canceled {
			status = "cancelled"
			err = context.Canceled
		} else if r.ctx.Err() == context.DeadlineExceeded {
			status = "timed_out"
			err = context.DeadlineExceeded
		}
		if err != nil {
			result.Finish = status
		}
		if result.Finish == "" {
			result.Finish = status
		}
		e.emit(r, "turn/end", map[string]any{"finish_reason": result.Finish, "status": status})
		events, _ := e.disk.Events(r.snapshot.ID, 0, 256)
		result.Events = events
		r.mu.Lock()
		r.snapshot.Status = status
		r.snapshot.Result = &result
		if err != nil {
			message := err.Error()
			if spec.Provider != nil && spec.Provider.APIKey != "" {
				message = strings.ReplaceAll(message, spec.Provider.APIKey, "[REDACTED]")
			}
			r.snapshot.Error = message
		}
		r.snapshot.Updated = time.Now().UTC()
		_ = e.disk.Save(r.snapshot)
		r.mu.Unlock()
		close(r.done)
		r.cancel()
	}()
	e.emit(r, "run/queued", map[string]any{"session_id": spec.SessionID})
	if spec.Kind == "batch" {
		result, err = e.batch(r, spec)
		return
	}
	sessionKey := hash([]any{spec.SessionID, spec.Rules, spec.Provider.Protocol, spec.Provider.BaseURL, spec.Provider.Model, spec.Scope})
	// A DSH workflow owns an isolated home. The shared Go service must keep
	// that boundary even when a logical stage/session name repeats.
	if spec.SessionNamespace != "" {
		sessionKey = hash([]any{spec.SessionNamespace, sessionKey})
	}
	unlock, lockErr := e.lockSession(r.ctx, sessionKey)
	if lockErr != nil {
		err = lockErr
		return
	}
	defer unlock()
	result, err = e.loop(r, spec, sessionKey)
}
func (e *Engine) batch(r *record, spec model.Spec) (model.Result, error) {
	e.state(r, "waiting_children")
	children := []string{}
	for i, child := range spec.Children {
		child.ParentID = r.snapshot.ID
		if child.SessionNamespace == "" {
			child.SessionNamespace = spec.SessionNamespace
		}
		if child.ClientID == "" {
			child.ClientID = fmt.Sprintf("%s/child-%d", spec.ClientID, i+1)
		}
		snapshot, err := e.Start(child)
		if err != nil {
			for _, id := range children {
				_ = e.Cancel(id)
			}
			return model.Result{}, err
		}
		children = append(children, snapshot.ID)
		e.emit(r, "child/spawned", map[string]any{"run_id": snapshot.ID, "index": i + 1})
	}
	type answer struct {
		index int
		s     model.Snapshot
		err   error
	}
	answers := make(chan answer, len(children))
	for i, id := range children {
		go func(i int, id string) { s, err := e.Wait(r.ctx, id); answers <- answer{i, s, err} }(i, id)
	}
	parts := make([]string, len(children))
	usage := model.Usage{Known: true}
	var failure error
	for range children {
		a := <-answers
		if a.err != nil || a.s.Status != "completed" {
			if failure == nil {
				failure = errors.New("child task failed; drafts retained")
				for _, id := range children {
					_ = e.Cancel(id)
				}
			}
			continue
		}
		parts[a.index] = a.s.Result.Text
		usage.Add(a.s.Result.Usage)
	}
	if failure != nil {
		return model.Result{Usage: usage}, failure
	}
	separator := spec.Separator
	if separator == "" {
		separator = "|\n"
	}
	return model.Result{Text: strings.Join(parts, separator), Usage: usage, Finish: "completed"}, nil
}
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.cancel()
	e.mu.Unlock()
	_ = e.tools.Close()
	e.wg.Wait()
}
