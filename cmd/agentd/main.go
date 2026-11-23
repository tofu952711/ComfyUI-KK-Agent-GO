package main

import (
	"context"
	"flag"
	"kkagent/internal/agent"
	"kkagent/internal/api"
	"kkagent/internal/asset"
	"kkagent/internal/config"
	"kkagent/internal/mcp"
	"kkagent/internal/provider"
	"kkagent/internal/store"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	path := flag.String("config", "", "JSON configuration path")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	assets, err := asset.New(filepath.Join(cfg.DataDir, "assets"), cfg.MaxResponseBytes)
	if err != nil {
		log.Fatal(err)
	}
	disk, err := store.New(cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	tools := mcp.New(cfg.MaxWorkers, cfg.MaxResponseBytes, cfg.CacheBytes, assets)
	engine, err := agent.New(cfg, provider.New(assets, cfg.MaxResponseBytes), tools, assets, disk)
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Close()
	apiServer := &api.Server{Engine: engine, Config: cfg}
	server := &http.Server{Addr: cfg.Listen, Handler: apiServer.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		engine.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("KKagent-go listening on http://%s; DSH/Harness disabled", cfg.Listen)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
