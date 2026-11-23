package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"kkagent/internal/agent"
	"kkagent/internal/config"
	"kkagent/internal/model"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Engine *agent.Engine
	Config config.Config
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		send(w, 200, map[string]any{"ok": true, "name": "KKagent-go", "version": "0.1.4", "api_version": 1, "image_transport": "dsh-compatible-1mib-jpeg85", "wire_compatibility": "dsh-video", "session_isolation": "workflow-namespace"})
	})
	mux.HandleFunc("POST /v1/runs", s.create)
	mux.HandleFunc("GET /v1/runs/{id}", s.get)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/runs/{id}/children", s.child)
	mux.HandleFunc("GET /v1/runs/{id}/events", s.events)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			send(w, 403, map[string]string{"error": "browser-origin requests are disabled"})
			return
		}
		if r.URL.Path != "/health" && s.Config.AuthToken != "" {
			actual := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(actual), []byte(s.Config.AuthToken)) != 1 {
				send(w, 401, map[string]string{"error": "invalid local service token"})
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func send(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) decode(w http.ResponseWriter, r *http.Request) (model.Spec, error) {
	var spec model.Spec
	r.Body = http.MaxBytesReader(w, r.Body, s.Config.MaxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return spec, fmt.Errorf("invalid request body")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return spec, fmt.Errorf("request must contain one JSON object")
	}
	return spec, nil
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	spec, err := s.decode(w, r)
	if err != nil {
		send(w, 400, map[string]string{"error": err.Error()})
		return
	}
	snapshot, err := s.Engine.Start(spec)
	if err != nil {
		send(w, 400, map[string]string{"error": err.Error()})
		return
	}
	send(w, 202, snapshot)
}
func (s *Server) child(w http.ResponseWriter, r *http.Request) {
	spec, err := s.decode(w, r)
	if err != nil {
		send(w, 400, map[string]string{"error": err.Error()})
		return
	}
	spec.ParentID = r.PathValue("id")
	snapshot, err := s.Engine.Start(spec)
	if err != nil {
		send(w, 400, map[string]string{"error": err.Error()})
		return
	}
	send(w, 202, snapshot)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.Engine.Get(r.PathValue("id"))
	if err != nil {
		send(w, 404, map[string]string{"error": "run not found"})
		return
	}
	send(w, 200, snapshot)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Cancel(r.PathValue("id")); err != nil {
		send(w, 404, map[string]string{"error": "run not found"})
		return
	}
	send(w, 202, map[string]bool{"ok": true})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Engine.Get(id); err != nil {
		send(w, 404, map[string]string{"error": "run not found"})
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if header := r.Header.Get("Last-Event-ID"); header != "" {
		after, _ = strconv.ParseInt(header, 10, 64)
	}
	if after < 0 {
		after = 0
	}
	if r.URL.Query().Get("format") == "json" {
		events, err := s.Engine.Events(id, after)
		if err != nil {
			send(w, 500, map[string]string{"error": "cannot read events"})
			return
		}
		send(w, 200, map[string]any{"events": events})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		send(w, 500, map[string]string{"error": "SSE unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.Engine.Events(id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			raw, _ := json.Marshal(event)
			if _, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Seq, event.Type, raw); err != nil {
				return
			}
			after = event.Seq
		}
		flusher.Flush()
		snapshot, err := s.Engine.Get(id)
		if err != nil || model.Terminal(snapshot.Status) && after >= snapshot.LastSeq {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-ticker.C:
		}
	}
}
