package mcp

import (
	"context"
	"encoding/json"
)

// Request is a JSON-RPC 2.0 request frame.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response frame.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// ResponseError represents the JSON-RPC error object.
type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func newResult(id json.RawMessage, result any) *Response {
	return &Response{JSONRPC: "2.0", ID: id, Result: result}
}

func newError(id json.RawMessage, code int, msg, data string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &ResponseError{Code: code, Message: msg, Data: data},
	}
}

// Implementation describes the server in the initialize handshake.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerCapabilities advertises optional features.
type ServerCapabilities struct {
	Tools map[string]any `json:"tools,omitempty"`
}

// InitializeResult is returned from the initialize method.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      Implementation     `json:"serverInfo"`
}

// ToolDefinition is the public schema of a tool.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListToolsResult is returned from tools/list.
type ListToolsResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// CallToolParams is the params object for tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Content is a single content block inside a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Optional fields used by some clients:
	Data     any    `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// CallToolResult is the result of a tool invocation.
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// ToolHandler executes a tool call and returns a CallToolResult.
type ToolHandler func(ctx context.Context, args json.RawMessage) (CallToolResult, error)

// Tool ties a definition to its handler.
type Tool struct {
	Definition ToolDefinition
	Handler    ToolHandler
}