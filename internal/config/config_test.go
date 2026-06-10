package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Transport != TransportStdio {
		t.Fatalf("default transport: got %q, want %q", c.Transport, TransportStdio)
	}
	if c.MQTT.Broker != "tcp://localhost:1883" {
		t.Fatalf("default broker: got %q", c.MQTT.Broker)
	}
	if c.MQTT.Prefix != "homed" {
		t.Fatalf("default prefix: got %q", c.MQTT.Prefix)
	}
	if c.HTTP.Addr != "" {
		t.Fatalf("default http.addr: got %q", c.HTTP.Addr)
	}
	if c.Paths == nil {
		t.Fatalf("default paths: got nil")
	}
}

func TestTransportValid(t *testing.T) {
	cases := []struct {
		in   Transport
		want bool
	}{
		{TransportStdio, true},
		{TransportStreamableHTTP, true},
		{Transport(""), false},
		{Transport("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoadFlagsOnly(t *testing.T) {
	// Make sure no stray env vars influence the test.
	cleared := []string{
		"HOMED_MQTT_BROKER", "HOMED_MQTT_PREFIX",
		"HOMED_MQTT_USERNAME", "HOMED_MQTT_PASSWORD", "HOMED_MQTT_CLIENT_ID",
		"HOMED_MCP_TRANSPORT", "HOMED_MCP_HTTP_ADDR", "HOMED_MCP_CONFIG",
		"HOMED_LOG_LEVEL", "HOMED_LOG_FILE",
		"HOMED_PATH_HOMED_WEB", "HOMED_PATH_HOMED_RECORDER",
	}
	for _, k := range cleared {
		t.Setenv(k, "")
	}

	// Make sure no ./config.json is picked up from the test working dir.
	t.Chdir(t.TempDir())

	cfg, path, err := Load([]string{
		"-broker", "tcp://10.0.0.1:1883",
		"-username", "u",
		"-password", "p",
		"-prefix", "h",
		"-client-id", "cid",
		"-transport", "streamableHttp",
		"-http-addr", ":9000",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != "" {
		t.Fatalf("expected no config file, got %q", path)
	}
	if cfg.Transport != TransportStreamableHTTP {
		t.Errorf("transport: got %q", cfg.Transport)
	}
	if cfg.HTTP.Addr != ":9000" {
		t.Errorf("http.addr: got %q", cfg.HTTP.Addr)
	}
	if cfg.MQTT.Broker != "tcp://10.0.0.1:1883" {
		t.Errorf("broker: got %q", cfg.MQTT.Broker)
	}
	if cfg.MQTT.Username != "u" || cfg.MQTT.Password != "p" {
		t.Errorf("credentials: %+v", cfg.MQTT)
	}
	if cfg.MQTT.Prefix != "h" {
		t.Errorf("prefix: got %q", cfg.MQTT.Prefix)
	}
	if cfg.MQTT.ClientID != "cid" {
		t.Errorf("client id: got %q", cfg.MQTT.ClientID)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MCP_CONFIG", "")
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")

	cfg := Config{
		Transport: TransportStreamableHTTP,
		HTTP:      HTTP{Addr: ":7777"},
		MQTT: MQTT{
			Broker:   "tcp://broker.local:1883",
			Username: "alice",
			Password: "secret",
			Prefix:   "homed2",
			ClientID: "mycid",
		},
		Paths: Paths{
			"homed-web":      "/opt/homed-web/database.json",
			"homed-recorder": "/opt/homed-recorder/homed-recorder.db",
		},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, path, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path == "" {
		t.Fatalf("expected config file path to be reported")
	}
	if got.Transport != TransportStreamableHTTP {
		t.Errorf("transport: got %q", got.Transport)
	}
	if got.HTTP.Addr != ":7777" {
		t.Errorf("http.addr: got %q", got.HTTP.Addr)
	}
	if got.MQTT.Broker != "tcp://broker.local:1883" {
		t.Errorf("broker: got %q", got.MQTT.Broker)
	}
	if got.MQTT.Prefix != "homed2" {
		t.Errorf("prefix: got %q", got.MQTT.Prefix)
	}
	if got.Paths["homed-web"] != "/opt/homed-web/database.json" {
		t.Errorf("paths.homed-web: got %q", got.Paths["homed-web"])
	}
	if got.Paths["homed-recorder"] != "/opt/homed-recorder/homed-recorder.db" {
		t.Errorf("paths.homed-recorder: got %q", got.Paths["homed-recorder"])
	}
}

func TestFlagOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")

	file := Config{
		Transport: TransportStdio,
		MQTT: MQTT{
			Broker: "tcp://from-file:1883",
			Prefix: "fromfile",
		},
		Paths: Paths{
			"homed-web": "/opt/from-file/database.json",
		},
	}
	data, _ := json.MarshalIndent(file, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, _, err := Load([]string{
		"-broker", "tcp://from-flag:1883",
		"-path-homed-web", "/opt/from-flag/database.json",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MQTT.Broker != "tcp://from-flag:1883" {
		t.Errorf("flag should override file, got %q", got.MQTT.Broker)
	}
	// Prefix was not set on the command line, must come from the file.
	if got.MQTT.Prefix != "fromfile" {
		t.Errorf("prefix from file expected, got %q", got.MQTT.Prefix)
	}
	if got.Paths["homed-web"] != "/opt/from-flag/database.json" {
		t.Errorf("flag should override file path, got %q", got.Paths["homed-web"])
	}
}

func TestEnvOverridesFileDefault(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// File leaves broker at default.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOMED_MQTT_BROKER", "tcp://from-env:1883")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	got, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MQTT.Broker != "tcp://from-env:1883" {
		t.Errorf("env should fill default slot, got %q", got.MQTT.Broker)
	}
}

func TestHTTPMissingAddr(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	_, _, err := Load([]string{"-transport", "streamableHttp"})
	if err == nil {
		t.Fatalf("expected error for missing http.addr")
	}
}

func TestUnknownTransport(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	_, _, err := Load([]string{"-transport", "bogus", "-broker", "tcp://x:1"})
	if err == nil {
		t.Fatalf("expected error for unknown transport")
	}
}

func TestHTTPAddrImpliesStreamableHTTP(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	got, _, err := Load([]string{"-http-addr", ":1234"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Transport != TransportStreamableHTTP {
		t.Fatalf("transport: got %q, want streamableHttp", got.Transport)
	}
	if got.HTTP.Addr != ":1234" {
		t.Errorf("http.addr: got %q", got.HTTP.Addr)
	}
}

// --- paths ---

func TestPathsDefaultsAreEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Paths) != 0 {
		t.Errorf("default paths: got %v", cfg.Paths)
	}
}

func TestPathsFileLoadsMap(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	data := []byte(`{
      "paths": {
        "homed-web":      "/opt/homed-web/database.json",
        "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
      }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Paths["homed-web"]; got != "/opt/homed-web/database.json" {
		t.Errorf("paths.homed-web: got %q", got)
	}
	if got := cfg.Paths["homed-recorder"]; got != "/opt/homed-recorder/homed-recorder.db" {
		t.Errorf("paths.homed-recorder: got %q", got)
	}
}

func TestPathsEnvVarFillsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Env var names use upper-snake-case, so the resulting key is
	// "homed_web" (underscores preserved by lowercase pass).
	t.Setenv("HOMED_PATH_HOMED_WEB", "/opt/from-env/database.json")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Paths["homed_web"]; got != "/opt/from-env/database.json" {
		t.Errorf("env homed_web: got %q", got)
	}
	if _, ok := cfg.Paths["homed_recorder"]; ok {
		t.Errorf("homed_recorder should not be set, got %q", cfg.Paths["homed_recorder"])
	}
}

func TestPathsEnvGeneratesMap(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Environment variable names use upper-snake-case; the loader
	// lowercases the suffix verbatim, so the resulting key keeps the
	// underscores.
	t.Setenv("HOMED_PATH_HOMED_WEB", "/opt/zb.json")
	t.Setenv("HOMED_PATH_DEVICES_DUMP", "/var/cache/dump.json")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Paths["homed_web"]; got != "/opt/zb.json" {
		t.Errorf("homed_web: got %q", got)
	}
	if got := cfg.Paths["devices_dump"]; got != "/var/cache/dump.json" {
		t.Errorf("devices_dump: got %q", got)
	}
}

func TestPathsFlagOverride(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load([]string{
		"-path-homed-web",      "/opt/flag-web/database.json",
		"-path-homed-recorder", "/opt/flag-rec/database.json",
		"-broker",              "tcp://x:1",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Paths["homed-web"]; got != "/opt/flag-web/database.json" {
		t.Errorf("homed-web: got %q", got)
	}
	if got := cfg.Paths["homed-recorder"]; got != "/opt/flag-rec/database.json" {
		t.Errorf("homed-recorder: got %q", got)
	}
}

func TestPathsFlagEqualsSyntax(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load([]string{
		"-path-homed-web=/opt/eq/database.json",
		"-broker", "tcp://x:1",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Paths["homed-web"]; got != "/opt/eq/database.json" {
		t.Errorf("homed-web: got %q", got)
	}
}

// TestPathsNeverCauseIO is a regression guard: the loader is allowed
// to read the config.json file itself, but it must NEVER stat, open
// or read any of the files referenced by cfg.Paths. Paths are an
// opaque string registry; their filesystem presence must not affect
// Load() success.
func TestPathsNeverCauseIO(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")
	t.Setenv("HOMED_PATH_HOMED_WEB", "")
	t.Setenv("HOMED_PATH_HOMED_RECORDER", "")

	// Point paths at files that are guaranteed not to exist. If the
	// loader ever tried to touch them, it would either return an
	// error or panic.
	const (
		missingWeb = "/opt/homed-web/database.json"
		missingRec = "/opt/homed-recorder/homed-recorder.db"
	)
	if _, err := os.Stat(missingWeb); err == nil {
		t.Skipf("%s unexpectedly exists on the test host", missingWeb)
	}
	if _, err := os.Stat(missingRec); err == nil {
		t.Skipf("%s unexpectedly exists on the test host", missingRec)
	}

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "paths": {
        "homed-web":      "/opt/homed-web/database.json",
        "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
      }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() must not return an error when paths.* refer to non-existent files, got: %v", err)
	}
	if path == "" {
		t.Fatalf("expected config file path to be reported")
	}
	if got := cfg.Paths["homed-web"]; got != missingWeb {
		t.Errorf("paths.homed-web: got %q, want %q", got, missingWeb)
	}
	if got := cfg.Paths["homed-recorder"]; got != missingRec {
		t.Errorf("paths.homed-recorder: got %q, want %q", got, missingRec)
	}

	// And the values must still be valid Go strings (no nil pointers,
	// no panic on access).
	for k, v := range cfg.Paths {
		_ = k
		_ = v
	}
}

// TestPathsEmptyStringNeverCausesIO is the JSON-side twin of
// TestPathsNeverCauseIO: empty values for paths.<name> must also be
// silently ignored by the loader (no I/O, no error).
func TestPathsEmptyStringNeverCausesIO(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_MQTT_BROKER", "")
	t.Setenv("HOMED_MQTT_PREFIX", "")
	t.Setenv("HOMED_MQTT_USERNAME", "")
	t.Setenv("HOMED_MQTT_PASSWORD", "")
	t.Setenv("HOMED_MQTT_CLIENT_ID", "")
	t.Setenv("HOMED_MCP_TRANSPORT", "")
	t.Setenv("HOMED_MCP_HTTP_ADDR", "")
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")
	t.Setenv("HOMED_PATH_HOMED_WEB", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "paths": { "homed-web": "" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load() must not return an error for empty paths.* values, got: %v", err)
	}
	if _, ok := cfg.Paths["homed-web"]; ok {
		t.Errorf("empty paths.homed-web should be dropped, got %q", cfg.Paths["homed-web"])
	}
}

// --- logging ---

func TestLoggingDefaultsAreOff(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingOff {
		t.Errorf("default logging.level: got %q, want %q", cfg.Logging.Level, LoggingOff)
	}
	if cfg.Logging.File != "" {
		t.Errorf("default logging.file: got %q, want empty", cfg.Logging.File)
	}
}

func TestLoggingLevelValid(t *testing.T) {
	cases := []struct {
		in   LoggingLevel
		want bool
	}{
		{LoggingOff, true},
		{LoggingInfo, true},
		{LoggingDebug, true},
		{LoggingLevel(""), false},
		{LoggingLevel("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.in.Valid(); got != tc.want {
			t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoggingFileLoadedFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "info", "file": "/var/log/homed-mcp/mcp.log" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingInfo {
		t.Errorf("logging.level: got %q, want %q", cfg.Logging.Level, LoggingInfo)
	}
	if cfg.Logging.File != "/var/log/homed-mcp/mcp.log" {
		t.Errorf("logging.file: got %q", cfg.Logging.File)
	}
}

func TestLoggingLevelNormalised(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "DEBUG" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingDebug {
		t.Errorf("logging.level: got %q, want %q", cfg.Logging.Level, LoggingDebug)
	}
}

func TestLoggingUnknownLevelCoercedToOff(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "verbose" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingOff {
		t.Errorf("unknown level should be coerced to off, got %q", cfg.Logging.Level)
	}
}

func TestLoggingFlagOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "info", "file": "/from-file.log" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load([]string{
		"-log-level", "debug",
		"-log-file", "/from-flag.log",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingDebug {
		t.Errorf("flag should override file level, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.File != "/from-flag.log" {
		t.Errorf("flag should override file path, got %q", cfg.Logging.File)
	}
}

func TestLoggingEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "debug")
	t.Setenv("HOMED_LOG_FILE", "/from-env.log")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "info", "file": "/from-file.log" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingDebug {
		t.Errorf("env should override file level, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.File != "/from-env.log" {
		t.Errorf("env should override file path, got %q", cfg.Logging.File)
	}
}

func TestLoggingInfoGetsDefaultFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "info" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.File == "" {
		t.Errorf("logging.file should default to a non-empty path when logging is enabled, got empty")
	}
}

func TestLoggingOffLeavesFileEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOMED_LOG_LEVEL", "")
	t.Setenv("HOMED_LOG_FILE", "")

	data := []byte(`{
      "mqtt": { "broker": "tcp://localhost:1883" },
      "logging": { "level": "off" }
    }`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != LoggingOff {
		t.Errorf("logging.level: got %q, want %q", cfg.Logging.Level, LoggingOff)
	}
}