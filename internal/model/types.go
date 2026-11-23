package model

import (
	"context"
	"encoding/json"
	"time"
)

type Block struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Data  string `json:"data,omitempty"`
	MIME  string `json:"mimeType,omitempty"`
	Asset string `json:"asset_ref,omitempty"`
}
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
type Message struct {
	Role    string          `json:"role"`
	Content []Block         `json:"content,omitempty"`
	Calls   []ToolCall      `json:"tool_calls,omitempty"`
	CallID  string          `json:"tool_call_id,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
	Raw     json.RawMessage `json:"provider_raw,omitempty"`
}
type Usage struct {
	Input  int64 `json:"inputTokens"`
	Output int64 `json:"outputTokens"`
	Total  int64 `json:"totalTokens"`
	Cached int64 `json:"cacheReadTokens,omitempty"`
	Known  bool  `json:"known"`
}

func (u *Usage) Add(v Usage) {
	u.Input += v.Input
	u.Output += v.Output
	u.Total += v.Total
	u.Cached += v.Cached
	u.Known = u.Known && v.Known
}

type ProviderConfig struct {
	WireCompatibility     string `json:"wire_compatibility,omitempty"`
	Protocol              string `json:"protocol"`
	BaseURL               string `json:"base_url"`
	Model                 string `json:"model"`
	APIKey                string `json:"api_key,omitempty"`
	APIKeyEnv             string `json:"api_key_env,omitempty"`
	Reasoning             string `json:"reasoning_effort,omitempty"`
	InstructionRole       string `json:"instruction_role,omitempty"`
	MaxTokens             int    `json:"max_tokens,omitempty"`
	StreamUsage           bool   `json:"stream_usage"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds,omitempty"`
	RetryCount            int    `json:"retry_count,omitempty"`
	MaxInlineRequestBytes int64  `json:"max_inline_request_bytes,omitempty"`
	ImageTransport        string `json:"image_transport,omitempty"`
}
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"inputSchema"`
}
type MCPConfig struct {
	ServerName string            `json:"serverName,omitempty"`
	Command    string            `json:"command"`
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	StartupMS  int               `json:"startupTimeoutMs,omitempty"`
	CallMS     int               `json:"toolCallTimeoutMs,omitempty"`
}
type Scope struct {
	ID      string    `json:"id"`
	JobID   string    `json:"job_id,omitempty"`
	Phase   string    `json:"phase,omitempty"`
	Segment int       `json:"segment_index,omitempty"`
	Start   float64   `json:"start,omitempty"`
	End     float64   `json:"end,omitempty"`
	MCP     MCPConfig `json:"mcp"`
}
type Limits struct {
	TimeoutSeconds     int `json:"timeout_seconds,omitempty"`
	MaxRounds          int `json:"max_model_rounds,omitempty"`
	MaxTools           int `json:"max_tool_calls,omitempty"`
	ToolTimeoutSeconds int `json:"tool_timeout_seconds,omitempty"`
}
type Spec struct {
	SessionNamespace string          `json:"session_namespace,omitempty"`
	ClientID         string          `json:"client_request_id"`
	SessionID        string          `json:"session_id"`
	ParentID         string          `json:"parent_run_id,omitempty"`
	Kind             string          `json:"kind,omitempty"`
	ProviderID       string          `json:"provider_config_id,omitempty"`
	Provider         *ProviderConfig `json:"provider,omitempty"`
	Rules            string          `json:"system_rules,omitempty"`
	Task             string          `json:"task,omitempty"`
	Evidence         string          `json:"media_evidence,omitempty"`
	Blocks           []Block         `json:"content,omitempty"`
	AssetRefs        []string        `json:"asset_refs,omitempty"`
	ScopeID          string          `json:"tool_scope_id,omitempty"`
	Scope            *Scope          `json:"tool_scope,omitempty"`
	AllowedTools     []string        `json:"allowed_tools,omitempty"`
	ToolsEnabled     bool            `json:"tools_enabled,omitempty"`
	Limits           Limits          `json:"limits"`
	Children         []Spec          `json:"children,omitempty"`
	Separator        string          `json:"separator,omitempty"`
}
type Turn struct {
	Message    Message
	Finish     string
	Usage      Usage
	FirstToken time.Duration
	Elapsed    time.Duration
}
type Request struct {
	Config   ProviderConfig
	Rules    string
	Messages []Message
	Tools    []ToolDefinition
}
type Delta func(string)
type Provider interface {
	Next(context.Context, Request, Delta) (Turn, error)
}
type ToolResult struct {
	Content []Block `json:"content"`
	IsError bool    `json:"isError,omitempty"`
}
type Tools interface {
	List(context.Context, Scope) ([]ToolDefinition, error)
	Execute(context.Context, Scope, ToolCall) (ToolResult, error)
	Release(Scope)
	Close() error
}
type Event struct {
	Seq  int64          `json:"seq"`
	Type string         `json:"type"`
	Time time.Time      `json:"time"`
	Data map[string]any `json:"data"`
}
type TokenRecord struct {
	Kind       string   `json:"kind"`
	RequestID  string   `json:"request_id"`
	Model      string   `json:"model"`
	Elapsed    float64  `json:"elapsed_seconds"`
	FirstToken float64  `json:"first_token_seconds"`
	Output     *int64   `json:"output_tokens"`
	TPS        *float64 `json:"average_tps"`
	Finish     string   `json:"finish_reason"`
	Usage      Usage    `json:"usage"`
}
type Result struct {
	Text       string        `json:"text"`
	Usage      Usage         `json:"usage"`
	Events     []Event       `json:"events,omitempty"`
	Finish     string        `json:"finish_reason"`
	TokenSpeed []TokenRecord `json:"token_speed"`
}
type Snapshot struct {
	ID        string    `json:"run_id"`
	ClientID  string    `json:"client_request_id"`
	SessionID string    `json:"session_id"`
	ParentID  string    `json:"parent_run_id,omitempty"`
	Children  []string  `json:"children,omitempty"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created_at"`
	Updated   time.Time `json:"updated_at"`
	Result    *Result   `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	LastSeq   int64     `json:"last_event_seq"`
	SpecHash  string    `json:"spec_hash,omitempty"`
}

func Terminal(s string) bool {
	return s == "completed" || s == "failed" || s == "cancelled" || s == "timed_out"
}
