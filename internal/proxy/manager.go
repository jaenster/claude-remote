package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

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
	client *client.Client
}

func NewManager(s *server.MCPServer) *Manager {
	return &Manager{
		server:   s,
		children: make(map[string]*child),
	}
}

func (m *Manager) StartChild(ctx context.Context, cfg config.ChildConfig) error {
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}

	c, err := client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
	if err != nil {
		return fmt.Errorf("creating client for %s: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "claude-remote",
		Version: "0.1.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("initializing %s: %w", cfg.Name, err)
	}

	toolsRes, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("listing tools for %s: %w", cfg.Name, err)
	}

	ch := &child{name: cfg.Name, client: c}

	for _, tool := range toolsRes.Tools {
		originalName := tool.Name
		proxied := tool
		proxied.Name = cfg.Name + "_" + tool.Name
		if proxied.Description != "" {
			proxied.Description = fmt.Sprintf("[%s] %s", cfg.Name, proxied.Description)
		}

		m.server.AddTool(proxied, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fwd := mcp.CallToolRequest{}
			fwd.Params.Name = originalName
			fwd.Params.Arguments = req.Params.Arguments
			return ch.client.CallTool(ctx, fwd)
		})
	}

	m.mu.Lock()
	m.children[cfg.Name] = ch
	m.mu.Unlock()

	slog.Info("child started", "name", cfg.Name, "tools", len(toolsRes.Tools))
	return nil
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, ch := range m.children {
		if err := ch.client.Close(); err != nil {
			slog.Error("closing child", "name", name, "err", err)
		}
	}
	m.children = make(map[string]*child)
}
