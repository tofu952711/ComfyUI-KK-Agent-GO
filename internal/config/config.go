package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"kkagent/internal/model"
	"net"
	"os"
	"path/filepath"
)

type Config struct {
	Listen           string                          `json:"listen"`
	DataDir          string                          `json:"data_dir"`
	AuthToken        string                          `json:"auth_token,omitempty"`
	MaxActive        int                             `json:"max_active_runs"`
	MaxLLM           int                             `json:"max_llm_requests"`
	MaxWorkers       int                             `json:"max_tool_workers"`
	MaxQueued        int                             `json:"max_queued_runs"`
	MaxRetained      int                             `json:"max_retained_runs"`
	MaxBodyBytes     int64                           `json:"max_body_bytes"`
	MaxResponseBytes int64                           `json:"max_response_bytes"`
	CacheBytes       int64                           `json:"cache_bytes"`
	Providers        map[string]model.ProviderConfig `json:"providers"`
	Scopes           map[string]model.Scope          `json:"tool_scopes"`
	Assets           map[string]string               `json:"assets"`
}

func Default() Config {
	return Config{Listen: "127.0.0.1:8190", DataDir: "data", MaxActive: 4, MaxLLM: 4, MaxWorkers: 2, MaxQueued: 256, MaxRetained: 128, MaxBodyBytes: 128 << 20, MaxResponseBytes: 128 << 20, CacheBytes: 16 << 20, Providers: map[string]model.ProviderConfig{}, Scopes: map[string]model.Scope{}, Assets: map[string]string{}}
}
func Load(path string) (Config, error) {
	c := Default()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	if value := os.Getenv("KKAGENT_TOKEN"); value != "" {
		c.AuthToken = value
	}
	if c.MaxActive < 1 || c.MaxLLM < 1 || c.MaxWorkers < 1 || c.MaxQueued < 1 || c.MaxRetained < 1 || c.MaxBodyBytes < 1024 || c.MaxResponseBytes < 1024 || c.CacheBytes < 0 {
		return c, fmt.Errorf("invalid resource limits")
	}
	host, _, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return c, e
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return c, fmt.Errorf("listen must use a loopback IP")
	}
	c.DataDir, e = filepath.Abs(c.DataDir)
	return c, e
}
