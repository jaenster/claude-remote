package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jaenster/claude-remote/internal/auth"
	"github.com/jaenster/claude-remote/internal/config"
	"github.com/jaenster/claude-remote/internal/proxy"
	"github.com/jaenster/claude-remote/internal/tools"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	mcpServer := server.NewMCPServer(
		cfg.Server.Name,
		cfg.Server.Version,
		server.WithToolCapabilities(true),
	)

	tools.RegisterExec(mcpServer, cfg.Sandbox.Workdir)
	tools.RegisterFiles(mcpServer)

	mgr := proxy.NewManager(mcpServer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, child := range cfg.Children {
		if err := mgr.StartChild(ctx, child); err != nil {
			slog.Error("failed to start child", "name", child.Name, "err", err)
		}
	}

	mcpHandler := server.NewStreamableHTTPServer(mcpServer,
		server.WithEndpointPath("/mcp"),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	var oauth *auth.OAuthServer
	if cfg.Auth.Issuer != "" && cfg.Auth.ClientID != "" {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			log.Fatal("DATABASE_URL required when OAuth is enabled")
		}
		store, err := auth.NewStore(ctx, dbURL)
		if err != nil {
			log.Fatalf("oauth store: %v", err)
		}
		defer store.Close()
		oauth = auth.NewOAuthServer(cfg.Auth, store)
		oauth.Register(mux)
	}

	mux.Handle("/", auth.Middleware(cfg.Auth, oauth, mcpHandler))

	httpServer := &http.Server{
		Addr:    *addr,
		Handler: mux,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down")
		cancel()
		mgr.StopAll()
		httpServer.Close()
	}()

	slog.Info("starting claude-remote", "addr", *addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
