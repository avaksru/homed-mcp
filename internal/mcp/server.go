// Package mcp implements a minimal Model Context Protocol server speaking
// JSON-RPC 2.0 over stdio. The protocol surface is intentionally small:
// initialize / listTools / callTool. It is fully compatible with the MCP
// specification version 2024-11-05.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/u236/homed-mcp/internal/logger"
)

// Server holds registered tools and dispatches requests.
//
// Tools are kept in a map for O(1) lookup, but a parallel slice
// remembers registration order so that tools/list is stable and
// predictable. Several MCP clients truncate or de-prioritise the
// tool catalogue when it is long; a stable order lets us put
// high-signal tools (e.g. homed_query_recorder) at the top.
type Server struct {
	name    string
	version string
	tools   map[string]Tool
	order   []string
	mu      sync.RWMutex
	log     *logger.Logger
}

// NewServer creates a server with the given identity.
func NewServer(name, version string) *Server {
	return &Server{
		name:    name,
		version: version,
		tools:   make(map[string]Tool),
	}
}

// WithLogger attaches a structured logger. The server does not
// retain a reference beyond its own field; passing nil clears it.
func (s *Server) WithLogger(l *logger.Logger) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = l
	return s
}

// logInfo writes a server-level info message. When no structured
// logger is configured the message is dropped on the floor Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ the
// stdio transport must not contaminate stdout with diagnostic
// output (stdout is reserved for JSON-RPC frames).
func (s *Server) logInfo(format string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Infof(format, args...)
}

// logDebug writes a server-level debug message.
func (s *Server) logDebug(format string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Debugf(format, args...)
}

// RegisterTool adds a tool to the server. Re-registering an
// existing tool updates it in place but does not change its
// position in the registration order.
func (s *Server) RegisterTool(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tools[t.Definition.Name]; !ok {
		s.order = append(s.order, t.Definition.Name)
	}
	s.tools[t.Definition.Name] = t
}

// Run reads JSON-RPC messages from stdin and writes responses to stdout
// until the stream is closed or the context is cancelled.
func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// MCP messages are newline-delimited JSON-RPC frames.
		line, err := readMessage(reader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.logDebug("stdio: parse error: %s (line=%s)", err, string(line))
			_ = enc.Encode(newError(nil, -32700, "parse error", err.Error()))
			continue
		}

		s.logDebug("stdio: <- %s id=%v", req.Method, req.ID)
		start := time.Now()
		resp := s.handle(&req)
		s.logInfo("stdio: -> %s id=%v duration=%s", req.Method, req.ID, time.Since(start))
		if resp == nil {
			// Notification, no response expected.
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func (s *Server) handle(req *Request) *Response {
	switch req.Method {
	case "initialize":
		s.logInfo("mcp: initialize client=%s %s", s.name, s.version)
		result := InitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities: ServerCapabilities{
				Tools: map[string]any{"listChanged": false},
			},
			ServerInfo: Implementation{
				Name:    s.name,
				Version: s.version,
			},
		}
		return newResult(req.ID, result)

	case "notifications/initialized":
		s.logInfo("mcp: client announced initialised")
		return nil

	case "ping":
		return newResult(req.ID, map[string]any{})

	case "tools/list":
		s.mu.RLock()
		list := make([]ToolDefinition, 0, len(s.order))
		for _, name := range s.order {
			if t, ok := s.tools[name]; ok {
				list = append(list, t.Definition)
			}
		}
		s.mu.RUnlock()
		s.logInfo("mcp: tools/list returned %d tools", len(list))
		return newResult(req.ID, ListToolsResult{Tools: list})

	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return newError(req.ID, -32602, "invalid params", err.Error())
		}
		s.mu.RLock()
		tool, ok := s.tools[params.Name]
		s.mu.RUnlock()
		if !ok {
			s.logInfo("mcp: tools/call unknown tool %q", params.Name)
			return newError(req.ID, -32602, "unknown tool", params.Name)
		}
		s.logInfo("mcp: tools/call %s args=%s", params.Name, string(params.Arguments))
		start := time.Now()
		out, err := tool.Handler(context.Background(), params.Arguments)
		dur := time.Since(start)
		if err != nil {
			s.logInfo("mcp: tools/call %s -> error after %s: %s", params.Name, dur, err)
			return newResult(req.ID, CallToolResult{
				Content: []Content{{Type: "text", Text: fmt.Sprintf("error: %s", err)}},
				IsError: true,
			})
		}
		s.logInfo("mcp: tools/call %s -> ok in %s (error=%v)", params.Name, dur, out.IsError)
		return newResult(req.ID, out)

	default:
		s.logInfo("mcp: unknown method %q", req.Method)
		return newError(req.ID, -32601, "method not found", req.Method)
	}
}

// readMessage reads a single newline-terminated JSON object. Blank lines are
// skipped to be tolerant of formatters that prepend a header.
func readMessage(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if len(line) == 0 {
				return nil, err
			}
			return nil, fmt.Errorf("read: %w", err)
		}
		trimmed := trimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		return trimmed, nil
	}
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// RunStdio is a convenience wrapper that wires the server to os.Stdin/Stdout.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.Run(ctx, os.Stdin, os.Stdout)
}