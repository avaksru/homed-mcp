package mcp

import (
	"bufio"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/u236/homed-mcp/internal/logger"
)

// newSessionID returns a 128-bit random hex string used as the
// Mcp-Session-Id value.
func newSessionID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// HTTPHandler exposes the Server over the MCP Streamable HTTP transport
// (specification version 2025-03-26). It implements:
//   - POST   /mcp  - send JSON-RPC requests (single or batch, JSON or SSE reply)
//   - GET    /mcp  - open an SSE stream for server-initiated notifications
//   - DELETE /mcp  - close the session
//   - GET    /     - tiny HTML status page (handy for manual debugging)
//   - GET    /healthz - returns 200 OK when the server is up
type HTTPHandler struct {
	srv     *Server
	logger  Logger
	log     *logger.Logger

	mu       sync.Mutex
	sessions map[string]*httpSession
}

// Logger is a minimal interface that mirrors the parts of log.Logger used
// by HTTPHandler. It is satisfied by the standard *log.Logger as well as
// logger.LoggerAdapter.
type Logger interface {
	Printf(format string, args ...any)
}

// NopLogger discards log messages.
var NopLogger Logger = nopLogger{}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

type httpSession struct {
	id      string
	created time.Time
	events  chan []byte
	done    chan struct{}
	once    sync.Once
}

func newHTTPSession(id string) *httpSession {
	return &httpSession{
		id:      id,
		created: time.Now(),
		events:  make(chan []byte, 32),
		done:    make(chan struct{}),
	}
}

func (s *httpSession) close() {
	s.once.Do(func() {
		close(s.done)
		close(s.events)
	})
}

// NewHTTPHandler wraps an MCP server into an http.Handler. The legacyLogger
// keeps backward compatibility with the previous signature; structured
// log entries (info/debug) are written via the structured log argument if
// non-nil.
func NewHTTPHandler(srv *Server, legacyLogger Logger) *HTTPHandler {
	if legacyLogger == nil {
		legacyLogger = NopLogger
	}
	return &HTTPHandler{
		srv:      srv,
		logger:   legacyLogger,
		sessions: make(map[string]*httpSession),
	}
}

// WithLogger attaches a structured logger to the handler and returns the
// same handler back. Passing nil clears the structured logger (the
// legacy log is still used for boot-time messages that flow before
// main has finished wiring up the structured logger).
//
// The handler returned by NewHTTPHandler is mutable; this method does
// not return a copy because the session map and the embedded mutex are
// value-typed and cannot be safely shared between two distinct
// HTTPHandler values. Returning a fresh copy with a freshly zero-valued
// mutex and a freshly nil session map caused a production panic
// ("assignment to entry in nil map") on the very first POST /mcp
// "initialize" call after a NewHTTPHandler(...).WithLogger(...) chain.
// Mutating in place keeps the chain fluent without breaking the
// invariants of the session state.
func (h *HTTPHandler) WithLogger(l *logger.Logger) *HTTPHandler {
	if h == nil {
		return nil
	}
	h.log = l
	return h
}

func (h *HTTPHandler) logInfo(format string, args ...any) {
	if h.log != nil {
		h.log.Infof(format, args...)
		return
	}
	h.logger.Printf(format, args...)
}

func (h *HTTPHandler) logDebug(format string, args ...any) {
	if h.log != nil {
		h.log.Debugf(format, args...)
	}
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Tiny landing page.
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		h.serveIndex(w, r)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if r.URL.Path != "/mcp" {
		h.logDebug("http: %s %s from %s -> 404", r.Method, r.URL.Path, r.RemoteAddr)
		http.NotFound(w, r)
		return
	}

	h.logInfo("http: %s %s from %s session=%q", r.Method, r.URL.Path, r.RemoteAddr, r.Header.Get("Mcp-Session-Id"))
	h.logDebug("http: headers=%v", r.Header)

	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r)
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	default:
		h.logInfo("http: method %s not allowed", r.Method)
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *HTTPHandler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>homed-mcp</title></head>
<body style="font-family:system-ui;max-width:640px;margin:2rem auto;padding:0 1rem">
<h1>homed-mcp</h1>
<p>Streamable HTTP transport is active.</p>
<p>POST JSON-RPC 2.0 messages to <code>/mcp</code>. Use the
<code>Accept: text/event-stream</code> header to receive responses as SSE.</p>
<p>See <a href="/healthz">/healthz</a> for liveness.</p>
</body></html>`)
}

func (h *HTTPHandler) sessionFor(id string) (*httpSession, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	return s, ok
}

func (h *HTTPHandler) putSession(s *httpSession) {
	h.mu.Lock()
	h.sessions[s.id] = s
	h.mu.Unlock()
	h.logInfo("http: new session %s", s.id)
}

func (h *HTTPHandler) dropSession(id string) {
	h.mu.Lock()
	if s, ok := h.sessions[id]; ok {
		s.close()
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	h.logInfo("http: session %s closed", id)
}

// handlePost accepts one or many JSON-RPC requests. If the client opted in to
// SSE via Accept, the reply is sent as an event stream (one event per
// response). Otherwise the replies are returned as a single JSON document.
func (h *HTTPHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		h.logInfo("http: read body: %s", err)
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		h.logInfo("http: empty body from %s", r.RemoteAddr)
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	h.logDebug("http: POST %d bytes from %s: %s", len(body), r.RemoteAddr, string(body))

	// Try to parse as a single request first, then as a batch.
	var requests []*Request
	if body[0] == '[' {
		if err := json.Unmarshal(body, &requests); err != nil {
			h.logInfo("http: invalid batch: %s", err)
			http.Error(w, "invalid batch: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		var one Request
		if err := json.Unmarshal(body, &one); err != nil {
			h.logInfo("http: invalid request: %s", err)
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		requests = []*Request{&one}
	}

	// Handle the "initialize" method specially to create a session id.
	sessionID := r.Header.Get("Mcp-Session-Id")
	hasInitialize := false
	for _, req := range requests {
		if req != nil && req.Method == "initialize" {
			hasInitialize = true
			break
		}
	}
	if hasInitialize && sessionID == "" {
		sessionID = newSessionID()
		h.putSession(newHTTPSession(sessionID))
	}
	if sessionID != "" {
		w.Header().Set("Mcp-Session-Id", sessionID)
	}

	// Process requests and collect responses.
	responses := make([]*Response, 0, len(requests))
	for _, req := range requests {
		if req == nil {
			continue
		}
		// inject server-managed context
		_ = req
		h.logDebug("http: dispatch %s id=%v", req.Method, req.ID)
		resp := h.srv.handle(req)
		if resp == nil {
			// Notification, no response.
			continue
		}
		// If we have a session, push the response onto its event channel
		// so any open GET stream can also receive it.
		if sessionID != "" {
			if s, ok := h.sessionFor(sessionID); ok {
				data, _ := json.Marshal(resp)
				select {
				case s.events <- data:
				default:
				}
			}
		}
		responses = append(responses, resp)
	}

	// Decide between JSON or SSE response based on Accept header.
	wantsSSE := acceptsSSE(r.Header.Get("Accept"))
	h.logDebug("http: responses=%d sse=%v", len(responses), wantsSSE)
	if wantsSSE {
		h.writeSSE(w, responses)
		return
	}
	h.writeJSON(w, requests, responses)
}

func (h *HTTPHandler) writeJSON(w http.ResponseWriter, requests []*Request, responses []*Response) {
	w.Header().Set("Content-Type", "application/json")
	if len(requests) == 1 && len(responses) == 1 {
		_ = json.NewEncoder(w).Encode(responses[0])
		return
	}
	if len(responses) == 0 {
		// All requests were notifications.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_ = json.NewEncoder(w).Encode(responses)
}

func (h *HTTPHandler) writeSSE(w http.ResponseWriter, responses []*Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	for _, resp := range responses {
		data, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
	}
	flusher.Flush()
}

// handleGet streams server-initiated notifications as SSE. The stream stays
// open until the client disconnects or the session is deleted.
func (h *HTTPHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if !acceptsSSE(r.Header.Get("Accept")) {
		http.Error(w, "Accept must include text/event-stream", http.StatusBadRequest)
		return
	}
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id header is required", http.StatusBadRequest)
		return
	}
	session, ok := h.sessionFor(sessionID)
	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Initial keep-alive comment so the client sees headers immediately.
	_, _ = fmt.Fprint(w, ": stream open\n\n")
	flusher.Flush()

	pinger := time.NewTicker(15 * time.Second)
	defer pinger.Stop()

	ctx := r.Context()
	h.logInfo("http: SSE stream open for session %s", sessionID)
	for {
		select {
		case <-ctx.Done():
			h.logInfo("http: SSE stream closed for session %s (client gone)", sessionID)
			return
		case <-session.done:
			h.logInfo("http: SSE stream closed for session %s (session ended)", sessionID)
			return
		case <-pinger.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case data, ok := <-session.events:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func (h *HTTPHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.dropSession(sessionID)
	w.WriteHeader(http.StatusNoContent)
}

// acceptsSSE returns true if the header lists text/event-stream explicitly.
func acceptsSSE(header string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		if strings.HasPrefix(strings.TrimSpace(part), "text/event-stream") {
			return true
		}
	}
	return false
}

// SessionIDs returns the currently known session ids (for tests / debug).
func (h *HTTPHandler) SessionIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// RunHTTP starts an http.Server with the given address. It blocks until
// ctx is cancelled or the server fails to start.
func RunHTTP(ctx context.Context, addr string, handler *HTTPHandler) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// SSEWriter is a tiny helper used by tests to assert on streamed events.
type SSEWriter struct {
	w *bufio.Writer
}

// WriteEvent writes a single SSE event followed by a flush. Provided for
// tests and potential future helpers.
func (s *SSEWriter) WriteEvent(event string, data []byte) error {
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	return s.w.Flush()
}