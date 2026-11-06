package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jaenster/claude-remote/internal/config"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type Manager struct {
	server   *server.MCPServer
	children map[string]*child
	mu       sync.Mutex
}

type child struct {
	name   string
	cfg    config.ChildConfig
	client *client.Client
	mu     sync.Mutex
}

func NewManager(s *server.MCPServer) *Manager {
	return &Manager{
		server:   s,
		children: make(map[string]*child),
	}
}

func (m *Manager) StartChild(ctx context.Context, cfg config.ChildConfig) error {
	c, tools, err := startChildProcess(ctx, cfg)
	if err != nil {
		return err
	}

	ch := &child{name: cfg.Name, cfg: cfg, client: c}

	for _, tool := range tools {
		originalName := tool.Name
		proxied := tool
		proxied.Name = cfg.Name + "_" + tool.Name
		if proxied.Description != "" {
			proxied.Description = fmt.Sprintf("[%s] %s", cfg.Name, proxied.Description)
		}

		m.server.AddTool(proxied, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			ch.mu.Lock()
			cl := ch.client
			ch.mu.Unlock()

			fwd := mcp.CallToolRequest{}
			fwd.Params.Name = originalName
			fwd.Params.Arguments = req.Params.Arguments
			result, err := cl.CallTool(ctx, fwd)
			if err != nil {
				slog.Warn("child call failed, restarting", "name", ch.name, "err", err)
				if restartErr := m.restartChild(ctx, ch); restartErr != nil {
					return nil, fmt.Errorf("child %s died and restart failed: %w", ch.name, restartErr)
				}
				ch.mu.Lock()
				cl = ch.client
				ch.mu.Unlock()
				return cl.CallTool(ctx, fwd)
			}
			return result, nil
		})
	}

	m.mu.Lock()
	m.children[cfg.Name] = ch
	m.mu.Unlock()

	slog.Info("child started", "name", cfg.Name, "tools", len(tools))
	return nil
}

func (m *Manager) restartChild(ctx context.Context, ch *child) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	if ch.client != nil {
		ch.client.Close()
	}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		c, _, err := startChildProcess(ctx, ch.cfg)
		if err == nil {
			ch.client = c
			slog.Info("child restarted", "name", ch.name, "attempt", attempt+1)
			return nil
		}
		lastErr = err
		slog.Warn("child restart failed", "name", ch.name, "attempt", attempt+1, "err", err)
		time.Sleep(time.Duration(attempt+1) * time.Second)
	}
	return fmt.Errorf("restart %s after 5 attempts: %w", ch.name, lastErr)
}

func startChildProcess(ctx context.Context, cfg config.ChildConfig) (*client.Client, []mcp.Tool, error) {
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
	if err != nil {
		return nil, nil, fmt.Errorf("creating client for %s: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "claude-remote",
		Version: "0.1.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return nil, nil, fmt.Errorf("initializing %s: %w", cfg.Name, err)
	}

	toolsRes, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, nil, fmt.Errorf("listing tools for %s: %w", cfg.Name, err)
	}

	return c, toolsRes.Tools, nil
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, ch := range m.children {
		ch.mu.Lock()
		if err := ch.client.Close(); err != nil {
			slog.Error("closing child", "name", name, "err", err)
		}
		ch.mu.Unlock()
	}
	m.children = make(map[string]*child)
}
