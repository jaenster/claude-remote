package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func RegisterFiles(s *server.MCPServer) {
	readTool := mcp.NewTool("read_file",
		mcp.WithDescription("Read a file from the sandbox filesystem"),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute or workspace-relative file path")),
	)
	s.AddTool(readTool, readFileHandler)

	writeTool := mcp.NewTool("write_file",
		mcp.WithDescription("Write content to a file in the sandbox filesystem"),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute or workspace-relative file path")),
		mcp.WithString("content", mcp.Required(), mcp.Description("File content to write")),
	)
	s.AddTool(writeTool, writeFileHandler)

	listTool := mcp.NewTool("list_files",
		mcp.WithDescription("List files in a directory"),
		mcp.WithString("path", mcp.Description("Directory path (defaults to working directory)")),
	)
	s.AddTool(listTool, listFilesHandler)
}

func readFileHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("read error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(data)), nil
}

func writeFileHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError("path is required"), nil
	}
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError("content is required"), nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("mkdir error: %v", err)), nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("write error: %v", err)), nil
	}
	return mcp.NewToolResultText("ok"), nil
}

func listFilesHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path := req.GetString("path", ".")
	entries, err := os.ReadDir(path)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("list error: %v", err)), nil
	}
	var lines []string
	for _, e := range entries {
		info, _ := e.Info()
		if info == nil {
			continue
		}
		kind := "-"
		if e.IsDir() {
			kind = "d"
		}
		lines = append(lines, fmt.Sprintf("%s %10d %s %s",
			kind, info.Size(), info.ModTime().Format(time.RFC3339), e.Name()))
	}
	if len(lines) == 0 {
		return mcp.NewToolResultText("(empty directory)"), nil
	}
	return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
}
