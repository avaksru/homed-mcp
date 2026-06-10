package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*httptest.Server, *HTTPHandler) {
	t.Helper()
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/light/kitchen": []byte(`{"name":"kitchen"}`),
		},
	}
	srv := NewServer("test", "0.0.0")
	RegisterHOMEdTools(srv, client, nil)
	h := NewHTTPHandler(srv, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, h
}

func TestHTTPNonStreamable(t *testing.T) {
	ts, _ := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	resp, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Error("missing Mcp-Session-Id header")
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type=%q", got)
	}

	var r Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.ID == nil {
		t.Error("missing response id")
	}
}

func TestHTTPSSE(t *testing.T) {
	ts, _ := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type=%q", got)
	}

	sc := bufio.NewScanner(resp.Body)
	found := false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var r Response
			if err := json.Unmarshal([]byte(data), &r); err == nil && r.ID != nil {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("no SSE data event found in body")
	}
}

func TestHTTPToolsList(t *testing.T) {
	ts, _ := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	session := resp.Header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatal("no session id")
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	body2 := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body2))
	req2.Header.Set("Accept", "application/json")
	req2.Header.Set("Mcp-Session-Id", session)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp2.Body)
		t.Fatalf("status=%d body=%s", resp2.StatusCode, buf)
	}
	var r Response
	if err := json.NewDecoder(resp2.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.ID == nil {
		t.Error("missing response id")
	}
}

func TestHTTPDeleteSession(t *testing.T) {
	ts, h := newTestServer(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	session := resp.Header.Get("Mcp-Session-Id")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if len(h.SessionIDs()) != 1 {
		t.Fatalf("want 1 session, got %d", len(h.SessionIDs()))
	}

	del, _ := http.NewRequest(http.MethodDelete, ts.URL+"/mcp", nil)
	del.Header.Set("Mcp-Session-Id", session)
	dresp, err := http.DefaultClient.Do(del)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	dresp.Body.Close()
	if dresp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status=%d", dresp.StatusCode)
	}
	if len(h.SessionIDs()) != 0 {
		t.Errorf("session not removed, have %d", len(h.SessionIDs()))
	}
}

func TestHTTPHealthz(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestHTTPPingBatch(t *testing.T) {
	ts, _ := newTestServer(t)
	body := `[
		{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}},
		{"jsonrpc":"2.0","id":2,"method":"ping"}
	]`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, buf)
	}
	var arr []*Response
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(arr) != 2 {
		t.Errorf("expected 2 responses in batch, got %d", len(arr))
	}
}

func TestRunHTTPShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	// RunHTTP uses its own listener; bind to :0 for an ephemeral port.
	go func() {
		srv := NewServer("x", "0.0.0")
		RegisterHOMEdTools(srv, nil, nil)
		err := RunHTTP(ctx, "127.0.0.1:0", NewHTTPHandler(srv, nil))
		done <- err
	}()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("shutdown error: %v", err)
	}
}

// TestHTTPInitializeAfterWithLogger is a regression test for the
// production panic "assignment to entry in nil map" raised in
// HTTPHandler.putSession when the server was wired up as
// NewHTTPHandler(srv, logger).WithLogger(structLogger). The previous
// implementation returned a fresh HTTPHandler copy with a zero-value
// mutex and a nil session map, so the very first POST /mcp
// "initialize" panicked inside putSession and the Cline client timed
// out. WithLogger must keep the original handler's session map intact.
func TestHTTPInitializeAfterWithLogger(t *testing.T) {
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/light/kitchen": []byte(`{"name":"kitchen"}`),
		},
	}
	srv := NewServer("test", "0.0.0")
	RegisterHOMEdTools(srv, client, nil)
	h := NewHTTPHandler(srv, nil).WithLogger(nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	if got := len(h.SessionIDs()); got != 0 {
		t.Fatalf("expected no sessions before initialize, got %d", got)
	}

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"Cline","version":"3.88.1"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, buf)
	}
	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Fatal("missing Mcp-Session-Id header")
	}
	// Drain the SSE body so the connection is closed cleanly.
	io.Copy(io.Discard, resp.Body)

	if got := len(h.SessionIDs()); got != 1 {
		t.Fatalf("expected 1 session after initialize, got %d (the handler lost its session map)", got)
	}
}

// TestWithLoggerMutatesInPlace guards the contract of WithLogger: it
// must operate on the same *HTTPHandler it was called on, not on a
// defensive copy. Returning a copy would silently produce a handler
// with a private (or nil) session map and a brand-new mutex, which is
// exactly what caused the production nil-map panic.
func TestWithLoggerMutatesInPlace(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	h := NewHTTPHandler(srv, nil)
	got := h.WithLogger(nil)
	if got != h {
		t.Fatalf("WithLogger must return the same *HTTPHandler, got %p want %p", got, h)
	}
	if h.sessions == nil {
		t.Fatalf("WithLogger cleared the session map; the chain NewHTTPHandler(...).WithLogger(...) would panic on first initialize")
	}
}

// Suppress unused imports in case some helpers are pruned.
var _ = bytes.NewBuffer
