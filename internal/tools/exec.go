package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type process struct {
	ID        int
	Command   string
	Stdout    *bytes.Buffer
	Stderr    *bytes.Buffer
	Cmd       *exec.Cmd
	Done      chan struct{}
	ExitCode  int
	Err       error
	StartedAt time.Time
}

var (
	processes   = make(map[int]*process)
	processMu   sync.Mutex
	nextProcID  int
)

func RegisterExec(s *server.MCPServer, defaultWorkdir string) {
	execTool := mcp.NewTool("exec",
		mcp.WithDescription("Execute a shell command. Set background=true to run without blocking."),
		mcp.WithString("command", mcp.Required(), mcp.Description("Shell command to execute")),
		mcp.WithString("workdir", mcp.Description("Working directory (defaults to /workspace)")),
		mcp.WithNumber("timeout", mcp.Description("Timeout in seconds (default 120, max 600). Ignored for background.")),
		mcp.WithBoolean("background", mcp.Description("Run in background and return a process ID immediately")),
	)
	s.AddTool(execTool, makeExecHandler(defaultWorkdir))

	statusTool := mcp.NewTool("exec_status",
		mcp.WithDescription("Check status and output of a background process"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("Process ID from a background exec call")),
		mcp.WithBoolean("tail", mcp.Description("Only return the last 100 lines of output")),
	)
	s.AddTool(statusTool, execStatusHandler)

	killTool := mcp.NewTool("exec_kill",
		mcp.WithDescription("Kill a background process"),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("Process ID to kill")),
	)
	s.AddTool(killTool, execKillHandler)

	listTool := mcp.NewTool("exec_list",
		mcp.WithDescription("List all tracked background processes"),
	)
	s.AddTool(listTool, execListHandler)
}

func makeExecHandler(defaultWorkdir string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		command, err := req.RequireString("command")
		if err != nil {
			return mcp.NewToolResultError("command is required"), nil
		}
		workdir := req.GetString("workdir", defaultWorkdir)
		background := req.GetBool("background", false)

		if background {
			return startBackground(command, workdir)
		}

		timeout := req.GetInt("timeout", 120)
		if timeout > 600 {
			timeout = 600
		}

		ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = workdir

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr := cmd.Run()
		exitCode := 0
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if runErr != nil {
			return mcp.NewToolResultError(fmt.Sprintf("exec failed: %v", runErr)), nil
		}

		result := fmt.Sprintf("exit_code: %d\n--- stdout ---\n%s", exitCode, stdout.String())
		if stderr.Len() > 0 {
			result += fmt.Sprintf("\n--- stderr ---\n%s", stderr.String())
		}
		return mcp.NewToolResultText(result), nil
	}
}

func startBackground(command, workdir string) (*mcp.CallToolResult, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workdir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("start failed: %v", err)), nil
	}

	processMu.Lock()
	nextProcID++
	id := nextProcID
	p := &process{
		ID:        id,
		Command:   command,
		Stdout:    &stdout,
		Stderr:    &stderr,
		Cmd:       cmd,
		Done:      make(chan struct{}),
		StartedAt: time.Now(),
	}
	processes[id] = p
	processMu.Unlock()

	go func() {
		err := cmd.Wait()
		p.Err = err
		if exitErr, ok := err.(*exec.ExitError); ok {
			p.ExitCode = exitErr.ExitCode()
		}
		close(p.Done)
	}()

	return mcp.NewToolResultText(fmt.Sprintf("started background process id=%d pid=%d", id, cmd.Process.Pid)), nil
}

func execStatusHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := req.GetInt("id", 0)
	if id == 0 {
		return mcp.NewToolResultError("id is required"), nil
	}

	processMu.Lock()
	p, ok := processes[id]
	processMu.Unlock()
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("no process with id %d", id)), nil
	}

	running := true
	select {
	case <-p.Done:
		running = false
	default:
	}

	stdout := p.Stdout.String()
	stderr := p.Stderr.String()
	tail := req.GetBool("tail", false)
	if tail {
		stdout = lastLines(stdout, 100)
		stderr = lastLines(stderr, 100)
	}

	status := "running"
	if !running {
		status = fmt.Sprintf("exited (code %d)", p.ExitCode)
	}

	result := fmt.Sprintf("id: %d\nstatus: %s\ncommand: %s\nstarted: %s\n--- stdout ---\n%s",
		p.ID, status, p.Command, p.StartedAt.Format(time.RFC3339), stdout)
	if len(stderr) > 0 {
		result += fmt.Sprintf("\n--- stderr ---\n%s", stderr)
	}
	return mcp.NewToolResultText(result), nil
}

func execKillHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id := req.GetInt("id", 0)
	if id == 0 {
		return mcp.NewToolResultError("id is required"), nil
	}

	processMu.Lock()
	p, ok := processes[id]
	processMu.Unlock()
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("no process with id %d", id)), nil
	}

	select {
	case <-p.Done:
		return mcp.NewToolResultText("process already exited"), nil
	default:
	}

	if err := p.Cmd.Process.Kill(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("kill failed: %v", err)), nil
	}
	return mcp.NewToolResultText("killed"), nil
}

func execListHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	processMu.Lock()
	defer processMu.Unlock()

	if len(processes) == 0 {
		return mcp.NewToolResultText("no tracked processes"), nil
	}

	var lines []string
	for _, p := range processes {
		status := "running"
		select {
		case <-p.Done:
			status = fmt.Sprintf("exited(%d)", p.ExitCode)
		default:
		}
		lines = append(lines, fmt.Sprintf("id=%d status=%s started=%s cmd=%s",
			p.ID, status, p.StartedAt.Format(time.RFC3339), p.Command))
	}
	return mcp.NewToolResultText(joinLines(lines)), nil
}

func lastLines(s string, n int) string {
	lines := splitLines(s)
	if len(lines) <= n {
		return s
	}
	return joinLines(lines[len(lines)-n:])
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}

