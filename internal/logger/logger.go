// Package logger provides a simple level-based logging facility for
// homed-mcp. Log records are written to a single file (or io.Discard
// when logging is disabled). The package is intentionally minimal вЂ”
// it is not meant to replace the standard library's log package, but
// to add level-based filtering and a clean per-level API on top of
// it.
//
// Three levels are supported:
//
//	LevelOff   вЂ” no log records are emitted at all.
//	LevelInfo  вЂ” application start-up information plus concise
//	             information about incoming requests.
//	LevelDebug вЂ” verbose, diagnostic information intended to make
//	             it possible to analyse and optimise the runtime
//	             behaviour of the application from the logs.
//
// All records are prefixed with a timestamp, the level name and a
// short "subsystem" tag supplied by the caller (e.g. "mqtt", "http",
// "tool") so that logs are easy to grep and to attribute to a
// specific component.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level is the verbosity of the logger.
type Level int

// Supported verbosity levels.
const (
	// LevelOff disables logging entirely. All log calls become no-ops.
	LevelOff Level = iota
	// LevelInfo logs application start-up and a minimal amount of
	// information about incoming requests.
	LevelInfo
	// LevelDebug logs every relevant detail: incoming requests,
	// processing steps, MQTT traffic, file I/O, ...
	LevelDebug
)

// String returns a human-readable name for the level.
func (l Level) String() string {
	switch l {
	case LevelOff:
		return "off"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	}
	return "unknown"
}

// ParseLevel converts a textual level ("off", "info", "debug",
// case-insensitive) into a Level. Unknown / empty values are treated
// as LevelOff so that a misconfigured configuration file can never
// cause the process to spam its logs.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "off", "":
		return LevelOff
	}
	return LevelOff
}

// Logger is the public API used by the rest of the application. It
// is safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	level Level
	out   io.WriteCloser
	// filePath is remembered so we can report it in start-up
	// messages without re-deriving it from out.
	filePath string
}

// New builds a Logger writing to the file at path. The file is
// opened in append mode and is created when missing. The parent
// directory is created when it does not exist. When level is
// LevelOff, no file is opened and all logging is silently dropped.
func New(level Level, path string) (*Logger, error) {
	l := &Logger{level: level}
	if level == LevelOff || strings.TrimSpace(path) == "" {
		l.out = nopCloser{io.Discard}
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logger: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("logger: open %s: %w", path, err)
	}
	l.out = f
	l.filePath = path
	return l, nil
}

// NewWithWriter builds a Logger that writes to w. This constructor
// is primarily intended for tests. It takes ownership of w: Close()
// will close it. Pass io.Discard to silence the logger.
func NewWithWriter(level Level, w io.WriteCloser) *Logger {
	if w == nil {
		w = nopCloser{io.Discard}
	}
	return &Logger{level: level, out: w}
}

// Close releases the underlying file (if any). Safe to call multiple
// times.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.out == nil {
		return nil
	}
	err := l.out.Close()
	l.out = nopCloser{io.Discard}
	return err
}

// Level returns the configured verbosity level.
func (l *Logger) Level() Level { return l.level }

// Path returns the on-disk path the logger writes to, or "" when
// logging is disabled.
func (l *Logger) Path() string { return l.filePath }

// Infof records an informational message. No-op at LevelOff.
func (l *Logger) Infof(format string, args ...any) {
	l.log(LevelInfo, "info", format, args...)
}

// Debugf records a debug message. No-op at LevelOff and LevelInfo.
func (l *Logger) Debugf(format string, args ...any) {
	l.log(LevelDebug, "debug", format, args...)
}

func (l *Logger) log(level Level, tag, format string, args ...any) {
	if l == nil || level == LevelOff || level > l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	line := fmt.Sprintf("%s %s %s\n", ts, tag, msg)
	l.mu.Lock()
	_, _ = io.WriteString(l.out, line)
	l.mu.Unlock()
}

// nopCloser adapts io.Discard (or any io.Writer) into an
// io.WriteCloser whose Close() is a no-op. The standard library does
// not provide one directly, hence the small wrapper.
type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }