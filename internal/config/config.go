// Package config loads application settings for homed-mcp from a JSON
// configuration file, environment variables and command-line flags.
//
// Precedence (highest priority first):
//  1. Command-line flag
//  2. Environment variable
//  3. Value from a configuration file (path supplied via -config flag or
//     HOMED_MCP_CONFIG; defaults to ./config.json next to the binary)
//  4. Built-in default
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Transport is the MCP transport to use.
type Transport string

const (
	// TransportStdio serves MCP over newline-delimited JSON-RPC on
	// stdin/stdout. This is the typical way MCP clients embed the
	// server as a child process.
	TransportStdio Transport = "stdio"

	// TransportStreamableHTTP serves MCP over the Streamable HTTP
	// transport (MCP spec 2025-03-26).
	TransportStreamableHTTP Transport = "streamableHttp"
)

// Valid reports whether t is a recognised transport name.
func (t Transport) Valid() bool {
	switch t {
	case TransportStdio, TransportStreamableHTTP:
		return true
	}
	return false
}

// HTTP holds the settings for the Streamable HTTP transport.
type HTTP struct {
	// Addr is the listen address accepted by net.Listen, e.g. ":8082"
	// or "0.0.0.0:8082". It is required when Transport is
	// TransportStreamableHTTP.
	Addr string `json:"addr"`
}

// MQTT holds the settings for the connection to the HOMEd MQTT broker.
type MQTT struct {
	// Broker is the broker URL accepted by Eclipse Paho, e.g.
	// "tcp://localhost:1883" or "ssl://broker.example.com:8883".
	Broker string `json:"broker"`

	// Username is the optional broker username.
	Username string `json:"username,omitempty"`

	// Password is the optional broker password.
	Password string `json:"password,omitempty"`

	// Prefix is the HOMEd topic prefix used for all subscriptions and
	// publications. Defaults to "homed" when empty.
	Prefix string `json:"prefix"`

	// ClientID is the MQTT client identifier. A random one is
	// generated when empty.
	ClientID string `json:"clientId,omitempty"`
}

// LoggingLevel is the verbosity of the application log. It is kept
// as a plain string so that the JSON configuration file stays
// human-readable and stable across versions.
type LoggingLevel string

// Recognised logging levels.
//
//   - LoggingOff   вЂ” the application is silent; no log file is
//     opened.
//   - LoggingInfo  вЂ” the application writes a concise start-up
//     banner plus a minimal description of every incoming request
//     to the log file. This is the recommended level for production
//     deployments.
//   - LoggingDebug вЂ” the application writes verbose diagnostic
//     information to the log file: every request, the matching
//     processing steps, the MQTT traffic (publishes, subscriptions
//     and inbound messages), file I/O, ... This level is meant for
//     development and performance analysis.
const (
	LoggingOff   LoggingLevel = "off"
	LoggingInfo  LoggingLevel = "info"
	LoggingDebug LoggingLevel = "debug"
)

// Valid reports whether l is a recognised logging level.
func (l LoggingLevel) Valid() bool {
	switch l {
	case LoggingOff, LoggingInfo, LoggingDebug:
		return true
	}
	return false
}

// Logging holds the logging settings. The defaults are populated by
// the loader so a configuration file that omits the section still
// yields a working logger.
type Logging struct {
	// Level selects the verbosity. Recognised values: "off"
	// (default), "info", "debug". Unknown values are coerced to
	// "off" so a typo in the configuration file can never cause
	// the process to start spamming the log file.
	Level LoggingLevel `json:"level"`

	// File is the path to the log file. When empty and Level is
	// not "off" the loader substitutes a sensible default
	// ("/var/log/homed-mcp/homed-mcp.log" on Unix, "homed-mcp.log"
	// next to the binary otherwise). When Level is "off" the File
	// value is ignored and no file is created.
	File string `json:"file"`
}

// Paths is a flat name в†’ filesystem-path map. The user is free to put
// here any local files (most commonly JSON files written by other
// HOMEd services such as homed-web, homed-recorder, вЂ¦) that MCP
// tools may need to reference at runtime.
//
// The MCP server does not open any of these files at start-up; it
// just records the map, validates the format, prints it to the log,
// and makes it available to tools and handlers.
//
// Example:
//
//	"paths": {
//	  "homed-web":      "/opt/homed-web/database.json",
//	  "homed-recorder": "/opt/homed-recorder/database.json"
//	}
type Paths map[string]string

// Config is the top-level configuration tree loaded from the file.
type Config struct {
	// Transport selects the MCP transport ("stdio" or
	// "streamableHttp"). Defaults to "stdio" for backward
	// compatibility.
	Transport Transport `json:"transport"`

	// HTTP contains the Streamable HTTP transport settings. It is
	// only consulted when Transport is TransportStreamableHTTP.
	HTTP HTTP `json:"http"`

	// MQTT contains the MQTT broker settings.
	MQTT MQTT `json:"mqtt"`

	// Logging contains the application log settings. A default
	// value (level=off) is always returned by Default().
	Logging Logging `json:"logging"`

	// Paths is a flat map of "name в†’ filesystem path" describing
	// local files the MCP server should be aware of.
	Paths Paths `json:"paths,omitempty"`
}

// Default returns the built-in defaults that match the previous
// hard-coded behaviour. Logging is off by default вЂ” operators must
// opt in by setting logging.level explicitly.
func Default() Config {
	return Config{
		Transport: TransportStdio,
		HTTP:      HTTP{Addr: ""},
		MQTT: MQTT{
			Broker:   "tcp://localhost:1883",
			Prefix:   "homed",
			ClientID: "",
		},
		Logging: Logging{
			Level: LoggingOff,
			File:  "",
		},
		Paths: Paths{},
	}
}

// defaultLogFile returns the platform-specific default location for
// the log file: /var/log/homed-mcp/homed-mcp.log on Unix and
// ./homed-mcp.log otherwise. The directory is not created here вЂ”
// the logger does that on its own.
func defaultLogFile() string {
	if runtime.GOOS == "windows" {
		return "homed-mcp.log"
	}
	return "/var/log/homed-mcp/homed-mcp.log"
}

// fileShape is the on-disk representation. It allows optional
// top-level fields so a partial file can be supplied.
type fileShape struct {
	Transport *Transport `json:"transport,omitempty"`
	HTTP      *struct {
		Addr *string `json:"addr,omitempty"`
	} `json:"http,omitempty"`
	MQTT *struct {
		Broker   *string `json:"broker,omitempty"`
		Username *string `json:"username,omitempty"`
		Password *string `json:"password,omitempty"`
		Prefix   *string `json:"prefix,omitempty"`
		ClientID *string `json:"clientId,omitempty"`
	} `json:"mqtt,omitempty"`
	Logging *struct {
		Level *string `json:"level,omitempty"`
		File  *string `json:"file,omitempty"`
	} `json:"logging,omitempty"`
	// Paths is accepted as a free-form stringв†’string map. Any JSON
	// value that is not a string is rejected by json.Unmarshal.
	Paths map[string]string `json:"paths,omitempty"`
}

// loadFile reads and parses the JSON config file at path. A missing
// file is not an error; an unreadable or malformed file is.
func loadFile(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return cfg, nil
	}
	var f fileShape
	if err := json.Unmarshal(data, &f); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if f.Transport != nil {
		cfg.Transport = *f.Transport
	}
	if f.HTTP != nil && f.HTTP.Addr != nil {
		cfg.HTTP.Addr = *f.HTTP.Addr
	}
	if f.MQTT != nil {
		if f.MQTT.Broker != nil {
			cfg.MQTT.Broker = *f.MQTT.Broker
		}
		if f.MQTT.Username != nil {
			cfg.MQTT.Username = *f.MQTT.Username
		}
		if f.MQTT.Password != nil {
			cfg.MQTT.Password = *f.MQTT.Password
		}
		if f.MQTT.Prefix != nil {
			cfg.MQTT.Prefix = *f.MQTT.Prefix
		}
		if f.MQTT.ClientID != nil {
			cfg.MQTT.ClientID = *f.MQTT.ClientID
		}
	}
	if f.Logging != nil {
		if f.Logging.Level != nil {
			cfg.Logging.Level = normaliseLoggingLevel(*f.Logging.Level)
		}
		if f.Logging.File != nil {
			cfg.Logging.File = *f.Logging.File
		}
	}
	for k, v := range f.Paths {
		if k == "" || v == "" {
			continue
		}
		if cfg.Paths == nil {
			cfg.Paths = Paths{}
		}
		cfg.Paths[k] = v
	}
	return cfg, nil
}

// normaliseLoggingLevel returns l unchanged when it matches one of
// the recognised values; otherwise it returns LoggingOff so the
// loader never accepts an unknown level silently.
func normaliseLoggingLevel(raw string) LoggingLevel {
	lvl := LoggingLevel(strings.ToLower(strings.TrimSpace(raw)))
	if !lvl.Valid() {
		return LoggingOff
	}
	return lvl
}

// envOr returns os.Getenv(key) if it is non-empty, otherwise def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envTransport reads the transport from the HOMED_MCP_TRANSPORT
// environment variable. Empty / unknown values are ignored.
func envTransport() (Transport, bool) {
	v := strings.TrimSpace(os.Getenv("HOMED_MCP_TRANSPORT"))
	if v == "" {
		return "", false
	}
	t := Transport(v)
	if !t.Valid() {
		return "", false
	}
	return t, true
}

// envLogging reads the logging settings from the HOMED_LOG_LEVEL
// and HOMED_LOG_FILE environment variables. Both are optional.
func envLogging() (Logging, bool) {
	lvl := os.Getenv("HOMED_LOG_LEVEL")
	file := os.Getenv("HOMED_LOG_FILE")
	if lvl == "" && file == "" {
		return Logging{}, false
	}
	out := Logging{}
	if lvl != "" {
		out.Level = normaliseLoggingLevel(lvl)
	}
	if file != "" {
		out.File = file
	}
	return out, true
}

// applyPathEnv walks os.Environ() and, for every variable with the
// HOMED_PATH_<KEY> prefix, copies it into the cfg.Paths map under the
// lowercased <KEY>. Variables whose KEY is empty or whose value is
// empty are ignored. Already-present entries are kept (the env
// override only fills empty slots).
func applyPathEnv(cfg *Config) {
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		name, val := kv[:eq], kv[eq+1:]
		const prefix = "HOMED_PATH_"
		if !strings.HasPrefix(name, prefix) || val == "" {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(name, prefix))
		if key == "" {
			continue
		}
		if cfg.Paths == nil {
			cfg.Paths = Paths{}
		}
		if _, exists := cfg.Paths[key]; !exists {
			cfg.Paths[key] = val
		}
	}
}

// splitPathFlags extracts any -path-<name> / -path-<name>=<value>
// pairs from args and returns both the remaining args (suitable for
// the standard flag package) and a map of path-name в†’ value. This
// lets the user pass an unbounded list of path entries without
// registering each one with flag.FlagSet explicitly.
func splitPathFlags(args []string) (rest []string, paths map[string]string) {
	paths = map[string]string{}
	rest = make([]string, 0, len(args))
	const p1 = "-path-"
	const p2 = "--path-"
	for i := 0; i < len(args); i++ {
		a := args[i]
		var key string
		switch {
		case strings.HasPrefix(a, p1):
			key = strings.TrimPrefix(a, p1)
		case strings.HasPrefix(a, p2):
			key = strings.TrimPrefix(a, p2)
		default:
			rest = append(rest, a)
			continue
		}
		var val string
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			val = args[i+1]
			i++
		}
		if key == "" || val == "" {
			continue
		}
		paths[strings.ToLower(key)] = val
	}
	return rest, paths
}

// Load builds the final configuration by applying command-line flags,
// environment variables and the configuration file on top of the
// built-in defaults. Flag parsing is performed in this function so
// every caller sees the same set of recognised flags.
func Load(args []string) (Config, string, error) {
	// Pull -path-<name>=... flags out before the standard flag
	// package sees them; they are dynamic and cannot be pre-registered.
	args, pathFlags := splitPathFlags(args)

	fs := flag.NewFlagSet("homed-mcp", flag.ContinueOnError)
	// Silence flag's own error printing; we will return errors to
	// the caller instead.
	fs.SetOutput(os.Stderr)

	var (
		configPath = fs.String("config", envOr("HOMED_MCP_CONFIG", ""), "Path to JSON configuration file (default: ./config.json if present)")
		transport  = fs.String("transport", "", "MCP transport: 'stdio' (default) or 'streamableHttp'")
		httpAddr   = fs.String("http-addr", "", "Streamable HTTP listen address, e.g. ':8082'. Implies transport=streamableHttp when set.")
		broker     = fs.String("broker", "", "MQTT broker URL (overrides config)")
		username   = fs.String("username", "", "MQTT username (overrides config)")
		password   = fs.String("password", "", "MQTT password (overrides config)")
		prefix     = fs.String("prefix", "", "HOMEd topic prefix (overrides config)")
		clientID   = fs.String("client-id", "", "MQTT client id (overrides config)")
		logLevel   = fs.String("log-level", "", "Logging level: 'off' (default), 'info' or 'debug' (overrides config)")
		logFile    = fs.String("log-file", "", "Path to the log file (overrides config). Ignored when log-level is 'off'.")
	)
	if err := fs.Parse(args); err != nil {
		return Config{}, "", err
	}

	// 1. Built-in defaults.
	cfg := Default()

	// 2. Configuration file (only when the path could be determined:
	// explicit flag, env var, or ./config.json next to the binary).
	filePath := *configPath
	if filePath == "" {
		if _, err := os.Stat("config.json"); err == nil {
			filePath = "config.json"
		}
	}
	if filePath != "" {
		loaded, err := loadFile(filePath)
		if err != nil {
			return Config{}, filePath, err
		}
		cfg = loaded
	}

	// 3. Environment variables.
	if cfg.MQTT.Broker == Default().MQTT.Broker {
		if v := os.Getenv("HOMED_MQTT_BROKER"); v != "" {
			cfg.MQTT.Broker = v
		}
	}
	if cfg.MQTT.Username == "" {
		cfg.MQTT.Username = os.Getenv("HOMED_MQTT_USERNAME")
	}
	if cfg.MQTT.Password == "" {
		cfg.MQTT.Password = os.Getenv("HOMED_MQTT_PASSWORD")
	}
	if cfg.MQTT.Prefix == Default().MQTT.Prefix {
		if v := os.Getenv("HOMED_MQTT_PREFIX"); v != "" {
			cfg.MQTT.Prefix = v
		}
	}
	if cfg.MQTT.ClientID == "" {
		cfg.MQTT.ClientID = os.Getenv("HOMED_MQTT_CLIENT_ID")
	}
	if t, ok := envTransport(); ok {
		cfg.Transport = t
	}
	if cfg.HTTP.Addr == "" {
		if v := os.Getenv("HOMED_MCP_HTTP_ADDR"); v != "" {
			cfg.HTTP.Addr = v
		}
	}
	if l, ok := envLogging(); ok {
		if l.Level != "" {
			cfg.Logging.Level = l.Level
		}
		if l.File != "" {
			cfg.Logging.File = l.File
		}
	}
	applyPathEnv(&cfg)

	// 4. Command-line flags (highest priority).
	if *transport != "" {
		cfg.Transport = Transport(*transport)
	}
	if *httpAddr != "" {
		cfg.HTTP.Addr = *httpAddr
	}
	if *broker != "" {
		cfg.MQTT.Broker = *broker
	}
	if *username != "" {
		cfg.MQTT.Username = *username
	}
	if *password != "" {
		cfg.MQTT.Password = *password
	}
	if *prefix != "" {
		cfg.MQTT.Prefix = *prefix
	}
	if *clientID != "" {
		cfg.MQTT.ClientID = *clientID
	}
	if *logLevel != "" {
		cfg.Logging.Level = normaliseLoggingLevel(*logLevel)
	}
	if *logFile != "" {
		cfg.Logging.File = *logFile
	}
	for k, v := range pathFlags {
		if v == "" {
			continue
		}
		if cfg.Paths == nil {
			cfg.Paths = Paths{}
		}
		cfg.Paths[k] = v
	}

	// Backward compatibility: -http-addr used to be the only way to
	// pick the streamable HTTP transport. Honour that here so the old
	// command lines keep working.
	if cfg.HTTP.Addr != "" && (cfg.Transport == "" || cfg.Transport == TransportStdio) {
		cfg.Transport = TransportStreamableHTTP
	}
	if cfg.Transport == "" {
		cfg.Transport = TransportStdio
	}

	if !cfg.Transport.Valid() {
		return Config{}, filePath, fmt.Errorf("config: unknown transport %q (want 'stdio' or 'streamableHttp')", cfg.Transport)
	}
	if cfg.Transport == TransportStreamableHTTP && cfg.HTTP.Addr == "" {
		return Config{}, filePath, errors.New("config: http.addr must be set when transport=streamableHttp")
	}
	if cfg.MQTT.Broker == "" {
		return Config{}, filePath, errors.New("config: mqtt.broker must be set")
	}
	if cfg.MQTT.Prefix == "" {
		cfg.MQTT.Prefix = "homed"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = LoggingOff
	}
	if cfg.Logging.Level != LoggingOff && cfg.Logging.File == "" {
		cfg.Logging.File = defaultLogFile()
	}
	if !cfg.Logging.Level.Valid() {
		return Config{}, filePath, fmt.Errorf("config: unknown logging.level %q (want 'off', 'info' or 'debug')", cfg.Logging.Level)
	}
	if cfg.Paths == nil {
		cfg.Paths = Paths{}
	}

	return cfg, filePath, nil
}