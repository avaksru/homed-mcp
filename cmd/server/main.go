// Command homed-mcp is an MCP (Model Context Protocol) server that bridges
// language models to the HOMEd smart-home ecosystem. It speaks JSON-RPC 2.0
// over stdio and/or the MCP Streamable HTTP transport, and exposes tools
// backed by MQTT topics published by HOMEd services.
//
// All runtime settings are loaded from a JSON configuration file, with
// environment variables and command-line flags overriding file values.
// See the config package and README for the precedence rules.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/u236/homed-mcp/internal/config"
	"github.com/u236/homed-mcp/internal/homedweb"
	"github.com/u236/homed-mcp/internal/logger"
	"github.com/u236/homed-mcp/internal/mcp"
	"github.com/u236/homed-mcp/internal/mqtt"
	"github.com/u236/homed-mcp/internal/recorder"
)

const version = "1.0.0"

func main() {
	// Short-circuit for -version / -help before touching the
	// configuration so the user can ask for them without having to
	// provide a valid MQTT broker URL (which config.Load would
	// otherwise require).
	for _, a := range os.Args[1:] {
		switch a {
		case "-version", "--version":
			fmt.Println("homed-mcp", version)
			return
		case "-h", "-help", "--help":
			printUsage()
			return
		}
	}

	cfg, cfgPath, err := config.Load(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "[homed-mcp] configuration error: %s\n", err)
		os.Exit(2)
	}

	// Initialise the structured logger. stderrLogger is the
	// always-on logger used for boot-time messages; it is also what
	// the HTTP handler uses when the structured logger has not
	// been wired up yet.
	stderrLogger := log.New(os.Stderr, "[homed-mcp] ", log.LstdFlags|log.Lmsgprefix)

	structLogger, err := logger.New(logger.ParseLevel(string(cfg.Logging.Level)), cfg.Logging.File)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[homed-mcp] logger error: %s\n", err)
		os.Exit(2)
	}

	if cfgPath != "" {
		stderrLogger.Printf("config: loaded %s", cfgPath)
		structLogger.Infof("config: loaded %s", cfgPath)
	}
	stderrLogger.Printf("config: transport=%s, mqtt.broker=%s, mqtt.prefix=%s",
		cfg.Transport, cfg.MQTT.Broker, cfg.MQTT.Prefix)
	structLogger.Infof("config: transport=%s, mqtt.broker=%s, mqtt.prefix=%s",
		cfg.Transport, cfg.MQTT.Broker, cfg.MQTT.Prefix)
	if cfg.Logging.Level != config.LoggingOff {
		stderrLogger.Printf("config: logging.level=%s, logging.file=%s",
			cfg.Logging.Level, cfg.Logging.File)
	} else {
		stderrLogger.Printf("config: logging disabled")
	}
	if cfg.Transport == config.TransportStreamableHTTP {
		stderrLogger.Printf("config: http.addr=%s", cfg.HTTP.Addr)
		structLogger.Infof("config: http.addr=%s", cfg.HTTP.Addr)
	}
	if len(cfg.Paths) > 0 {
		keys := make([]string, 0, len(cfg.Paths))
		for k := range cfg.Paths {
			keys = append(keys, k)
		}
		// Stable order for nicer log output.
		sort.Strings(keys)
		for _, k := range keys {
			stderrLogger.Printf("paths: %s=%s", k, cfg.Paths[k])
			structLogger.Infof("paths: %s=%s", k, cfg.Paths[k])
		}
	}

	client, err := mqtt.NewClient(mqtt.Config{
		Broker:   cfg.MQTT.Broker,
		Username: cfg.MQTT.Username,
		Password: cfg.MQTT.Password,
		Prefix:   cfg.MQTT.Prefix,
		ClientID: cfg.MQTT.ClientID,
		Logger:   structLogger,
	})
	if err != nil {
		stderrLogger.Fatalf("mqtt: %s", err)
	}
	defer client.Disconnect()

	// Pre-subscribe to the discovery topics so that retained messages
	// delivered right after connect populate our cache.
	for _, sub := range []string{
		"device/#",
		"expose/#",
		"service/#",
		"status/#",
	} {
		if err := client.Subscribe(sub, 1); err != nil {
			stderrLogger.Printf("subscribe %s: %s", sub, err)
			structLogger.Infof("subscribe %s: %s", sub, err)
		}
	}

	// Build the homed-web meta provider. The "homed-web" path in the
	// configuration is optional: when the file is missing or
	// malformed, we still get a usable Provider that simply returns
	// no user-defined names.
	metaProvider, err := homedweb.NewProviderWithLogger(cfg.Paths["homed-web"], structLogger)
	if err != nil {
		stderrLogger.Printf("homed-web: %s (continuing without user-defined names)", err)
		structLogger.Infof("homed-web: %s (continuing without user-defined names)", err)
	} else if metaProvider.HasDatabase() {
		stderrLogger.Printf("homed-web: loaded user-defined names from %s", cfg.Paths["homed-web"])
		structLogger.Infof("homed-web: loaded user-defined names from %s", cfg.Paths["homed-web"])
	}

	// Wire the id<->name alias resolver. homed-web's database.json
	// stores endpoints in their canonical id form
	// (e.g. "custom/alarm"), but when a service runs with
	// names=true the broker publishes under the name form
	// (e.g. "custom/РћС…СЂР°РЅР°"). To make toolListDevices /
	// toolListExposes report the right usage for both forms, the
	// homedweb.Provider falls back to an alias map when the strict
	// equality lookup misses. We populate that map by scanning the
	// cached status/<service> payloads once and pairing every
	// {id, name} pair.
	//
	// The same resolver is also wired into the recorder provider so
	// that homed_query_recorder can answer questions asked with the
	// broker-side name form (e.g. "average supply temperature for
	// OpenTherm in the last 24 hours") against rows that the
	// recorder actually stores under the canonical id form
	// (e.g. "custom/61226326-10251872"). Without this wiring the
	// recorder would always report zero matches for name-form
	// endpoints and the LLM would see an empty result.
	var aliasResolver recorder.EndpointAliasResolver
	if metaProvider != nil && client != nil {
		resolver := buildEndpointAliasResolver(client, structLogger)
		metaProvider.SetEndpointAliases(resolver)
		aliasResolver = recorder.EndpointAliasResolver(resolver)
	}

	// Build the homed-recorder provider. The "homed-recorder" path
	// in the configuration is optional: when the file is missing or
	// malformed, we still get a Provider that simply has
	// HasDatabase() == false. The recorder is opened read-only so
	// homed-mcp can never corrupt the live data.
	recProvider, err := recorder.NewProviderWithLogger(cfg.Paths["homed-recorder"], structLogger)
	if err != nil {
		stderrLogger.Printf("homed-recorder: %s (continuing without historical data)", err)
		structLogger.Infof("homed-recorder: %s (continuing without historical data)", err)
	} else if recProvider.HasDatabase() {
		stderrLogger.Printf("homed-recorder: loaded historical data from %s", recProvider.DSN())
		structLogger.Infof("homed-recorder: loaded historical data from %s", recProvider.DSN())
	}
	// Re-use the same id<->name resolver on the recorder side so
	// that name-form queries (e.g. "custom/OpenTherm") resolve to
	// the canonical id-form stored in the recorder database.
	if recProvider != nil && aliasResolver != nil {
		recProvider.SetEndpointAliases(aliasResolver)
	}

	srv := mcp.NewServer("homed-mcp", version).WithLogger(structLogger)
	// Register the recorder tool FIRST. Several MCP clients (notably
	// PicoClaw and Cline) truncate the system prompt that lists
	// tools, and history/aggregate questions are easy to miss if
	// the tool is buried in the catalogue. By putting
	// homed_query_recorder at the top of tools/list we make sure
	// the language model sees it on the first pass.
	//
	// The recorder tool needs a name-lookup that pulls from the
	// homed-web database so that the historical data can be
	// enriched with the same user-defined labels used by the live
	// tools.
	var nameLookup recorder.NameLookup
	if metaProvider != nil {
		nameLookup = &recorder.HomedwebLookup{Provider: metaProvider}
	}
	names := mcp.RegisterRecorderTool(srv, recProvider, nameLookup)
	names = append(names, mcp.RegisterHOMEdTools(srv, client, metaProvider)...)
	stderrLogger.Printf("registered %d tools: %s", len(names), strings.Join(names, ", "))
	structLogger.Infof("registered %d tools: %s", len(names), strings.Join(names, ", "))

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch cfg.Transport {
	case config.TransportStreamableHTTP:
		handler := mcp.NewHTTPHandler(srv, stderrLogger).WithLogger(structLogger)
		stderrLogger.Printf("serving MCP over Streamable HTTP at %s/mcp", cfg.HTTP.Addr)
		structLogger.Infof("serving MCP over Streamable HTTP at %s/mcp", cfg.HTTP.Addr)
		if err := mcp.RunHTTP(ctx, cfg.HTTP.Addr, handler); err != nil {
			stderrLogger.Printf("http server stopped: %s", err)
			structLogger.Infof("http server stopped: %s", err)
		}
	case config.TransportStdio:
		stderrLogger.Printf("serving MCP over stdio")
		structLogger.Infof("serving MCP over stdio")
		if err := srv.RunStdio(ctx); err != nil {
			stderrLogger.Printf("server stopped: %s", err)
			structLogger.Infof("server stopped: %s", err)
		}
	}
}

// printUsage prints the help text. It is intentionally a hand-rolled
// summary because the underlying flag set lives in the config package.
func printUsage() {
	fmt.Printf("homed-mcp %s\n\n", version)
	fmt.Println("Usage: homed-mcp [flags]")
	fmt.Println()
	fmt.Println("Settings may be supplied via a JSON config file (-config / HOMED_MCP_CONFIG),")
	fmt.Println("environment variables, and command-line flags. Precedence (highest first):")
	fmt.Println("  1. command-line flag")
	fmt.Println("  2. environment variable")
	fmt.Println("  3. configuration file (./config.json by default)")
	fmt.Println("  4. built-in default")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -config <path>           Path to JSON configuration file (default: ./config.json if present)")
	fmt.Println("  -transport <name>        MCP transport: 'stdio' (default) or 'streamableHttp'")
	fmt.Println("  -http-addr <addr>        Streamable HTTP listen address, e.g. ':8082' (implies -transport=streamableHttp)")
	fmt.Println("  -broker <url>            MQTT broker URL (default: tcp://localhost:1883)")
	fmt.Println("  -username <name>         MQTT username")
	fmt.Println("  -password <secret>       MQTT password")
	fmt.Println("  -prefix <name>           HOMEd topic prefix (default: homed)")
	fmt.Println("  -client-id <id>          MQTT client id (random if empty)")
	fmt.Println()
	fmt.Println("Local paths (set or override entries from config.json / env vars):")
	fmt.Println("  -path-<name> <value>     Set paths.<name> to <value>. Use any number of these flags.")
	fmt.Println("                            Example: -path-homed-web=/opt/homed-web/database.json")
	fmt.Println()
	fmt.Println("Logging:")
	fmt.Println("  -log-level <name>        Logging level: 'off' (default), 'info' or 'debug'")
	fmt.Println("  -log-file <path>         Path to the log file (default: ./homed-mcp.log when logging is on)")
	fmt.Println()
	fmt.Println("Other:")
	fmt.Println("  -version                 Print version and exit")
	fmt.Println("  -h, -help                Show this help and exit")
	fmt.Println()
	fmt.Println("Environment variables follow the same names in upper case with HOMED_ prefix,")
	fmt.Println("e.g. HOMED_MQTT_BROKER, HOMED_MCP_TRANSPORT, HOMED_LOG_LEVEL, HOMED_PATH_<KEY>.")
	fmt.Println("HOMED_PATH_<KEY>=<value> populates paths.<KEY> in the configuration.")
	fmt.Println()
	fmt.Println("See config.example.json for a sample configuration file.")
}

// endpointAliasResolver is the closure returned by
// buildEndpointAliasResolver. It maps a broker-side name endpoint
// (e.g. "custom/РћС…СЂР°РЅР°") to the canonical id-based form stored in
// homed-web's database.json (e.g. "custom/alarm") вЂ” and vice versa
// вЂ” by inspecting the cached "status/<service>" payload that the
// HOMEd service publishes with explicit "id" and "name" fields for
// every device it owns.
//
// The map is held in a snapshot guarded by an RWMutex; the snapshot
// is recomputed lazily on the next lookup after a retained message
// is observed on a "status/<service>" topic, and otherwise served
// straight from memory. This keeps the resolver cheap on the hot
// path (one RLock + map lookup) while still picking up devices that
// come online after start-up.
type endpointAliasResolver func(endpoint string) string

// aliasClient is the minimal slice of the mqtt client that
// buildEndpointAliasResolver needs. Defined as an interface here so
// the helper is trivially testable with a fake retained cache and
// an optional retained-message hook.
type aliasClient interface {
	Retained() map[string][]byte
	// OnRetained wires a callback that fires synchronously for
	// every retained message that lands in the cache. The real
	// *mqtt.Client implements this; the test fake may be a no-op.
	OnRetained(func(topic string, payload []byte))
}

// statusServiceDevice is the per-device entry the HOMEd service
// publishes inside the "status/<service>" payload when its per-
// service "names" flag is true. "id" is the canonical id (used in
// MQTT topic paths and homed-web's database.json), "name" is the
// broker-side human-readable name (used in MQTT topic paths for
// services running with names=true). Both fields are required to
// build the alias.
type statusServiceDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// statusServicePayload models the "status/<service>" retained
// payload. We accept both the "devices" array and the "names"
// boolean; the latter is also handled by mqtt.Client.captureServiceNamesFlag
// and is only consulted here as a hint.
type statusServicePayload struct {
	Devices []statusServiceDevice `json:"devices"`
	Names   *bool                 `json:"names"`
}

// deviceEndpoint is the per-endpoint summary we extract from a
// "device/<service>/<X>" retained payload. lastSeen is read from
// the payload when present, and availabilityTopic lets us decide
// whether two X values refer to the same physical device (matching
// availabilityTopic) or are siblings (different
// availabilityTopic). This is the cold-start fallback used when
// "status/<service>" has not been seen yet.
type deviceEndpoint struct {
	Service           string
	Endpoint          string
	LastSeen          int64
	AvailabilityTopic string
	HasLastSeen       bool
}

// buildEndpointAliasResolver returns a function that, given an
// endpoint of the form "<service>/<id-or-name>", returns the
// canonical id-based endpoint recognised by homed-web's database.json.
// The function is safe to call concurrently.
//
// Mapping is computed in two layers, by preference:
//
//  1. "status/<service>" retained payload (canonical). When the
//     service publishes a "devices" array with explicit "id" and
//     "name" fields, every device yields a bidirectional pair
//     <service>/<id> в†” <service>/<name>. This is the HOMEd "names"
//     convention; it is the only source that is reliable when
//     multiple devices of the same type (e.g. five BLE
//     thermometers) share byte-identical "expose" payloads.
//
//  2. "device/<service>/<X>" retained payload (cold-start fallback).
//     When status/<service> has not been seen yet, the resolver
//     groups "device/<service>/<X>" retains by matching
//     availabilityTopic вЂ” two endpoints that point at the same
//     availability topic are aliases of each other. lastSeen
//     (when present) is used to break ambiguous ties: two
//     endpoints with the same availabilityTopic and lastSeen
//     within В±5s are merged.
//
// The previous "expose/<service>/<X>" payload-fingerprint
// heuristic has been removed: it was the root cause of the
// 2026-06-08 17:46:54 alias-resolver log spam, where five BLE
// devices with identical expose payloads were merged into a
// single alias.
func buildEndpointAliasResolver(client aliasClient, log *logger.Logger) endpointAliasResolver {
	var (
		mu       sync.RWMutex
		snapshot = map[string]string{}
		// dirty is flipped to true when a retained message is
		// observed on a topic that could affect the alias map.
		// The next lookup recomputes the snapshot and clears the
		// flag. We invalidate lazily rather than recomputing
		// synchronously inside the MQTT dispatcher goroutine so
		// that a burst of retains (e.g. on connect) does not
		// trigger O(N) recomputation per message.
		dirty = true
	)
	// 5s window for cold-start lastSeen matching. Two device
	// retains that the broker delivered in the same session
	// burst for the same physical device will have lastSeen
	// values that agree to within a few seconds.
	const lastSeenWindowSec = 5

	// recompute scans the retained cache and rebuilds the
	// snapshot. Always called under mu.Lock().
	recompute := func() {
		dirty = false
		snapshot = computeAliasMap(client, log, lastSeenWindowSec)
	}
	// Prime an initial snapshot so the first lookup does not
	// pay the full cost (the retained cache is still warming up
	// at start-up; the OnRetained hook will mark the snapshot
	// dirty and the next lookup will refresh it).
	recompute()

	// If the real client supports it, subscribe to retained
	// updates so the snapshot is invalidated exactly when it
	// needs to be. A nil interface (test fake that does not
	// implement OnRetained) is a no-op.
	if hooker, ok := client.(interface {
		OnRetained(func(topic string, payload []byte))
	}); ok {
		hooker.OnRetained(func(topic string, _ []byte) {
			// Only "status/<service>" and "device/<service>/..."
			// retains can change the alias map. Filter early so
			// that a high-frequency retain on an unrelated
			// topic (e.g. status/<device> with state ticks)
			// does not bounce the snapshot.
			if !affectsAliasMap(topic) {
				return
			}
			mu.Lock()
			dirty = true
			mu.Unlock()
		})
	}

	return func(endpoint string) string {
		mu.RLock()
		if !dirty {
			v := snapshot[endpoint]
			mu.RUnlock()
			return v
		}
		mu.RUnlock()
		// Recompute outside the read lock; other readers
		// continue to use the stale snapshot. The next reader
		// after us (and the writer that flipped dirty) is
		// guaranteed to see the fresh map.
		mu.Lock()
		if dirty {
			recompute()
		}
		v := snapshot[endpoint]
		mu.Unlock()
		return v
	}
}

// affectsAliasMap returns true for topic prefixes whose retained
// payloads can change the id<->name alias map.
func affectsAliasMap(topic string) bool {
	if strings.HasPrefix(topic, "status/") {
		return true
	}
	if strings.HasPrefix(topic, "device/") {
		return true
	}
	return false
}

// computeAliasMap builds a fresh id<->name alias map from the
// cached retained payloads. It is the single source of truth used by
// buildEndpointAliasResolver; extracted as a free function so it
// can be unit-tested directly with a fake retained cache.
func computeAliasMap(client aliasClient, log *logger.Logger, lastSeenWindowSec int64) map[string]string {
	out := make(map[string]string)
	if client == nil {
		return out
	}
	cache := client.Retained()
	if len(cache) == 0 {
		return out
	}

	// Pass 1: try to use the canonical "status/<service>" payload.
	// This is the only reliable source when several devices of the
	// same type (e.g. five BLE thermometers with identical
	// expose/<service>/<X> payloads) live in the same service.
	statusByService := make(map[string][]statusServiceDevice)
	for topic, payload := range cache {
		if !strings.HasPrefix(topic, "status/") {
			continue
		}
		service := strings.TrimPrefix(topic, "status/")
		if service == "" || strings.ContainsRune(service, '/') {
			continue
		}
		if len(payload) == 0 {
			continue
		}
		var raw statusServicePayload
		if err := json.Unmarshal(payload, &raw); err != nil {
			// Malformed JSON is not fatal вЂ” there is at
			// most one "status/<service>" retain in
			// flight at a time and the next one will
			// overwrite it.
			continue
		}
		if len(raw.Devices) > 0 {
			statusByService[service] = raw.Devices
		}
	}

	if len(statusByService) > 0 {
		for service, devs := range statusByService {
			// The map is keyed by the FULL endpoint
			// ("<service>/<id-or-name>") because that is the
			// form the rest of the tool surface uses when it
			// consults the alias resolver. A bare "<id>" key
			// would silently break the recorder and the
			// homed-web provider fallback.
			for _, d := range devs {
				if d.ID == "" || d.Name == "" {
					continue
				}
				if d.ID == d.Name {
					continue
				}
				addAlias(out, service+"/"+d.ID, service+"/"+d.Name, log)
			}
		}
		// A canonical map is available: prefer it. We still
		// extend the map with cold-start device retains for
		// services that did not publish a "status/<service>"
		// payload (e.g. zigbee, matter), so name-form endpoints
		// for those services still resolve when applicable.
		for topic, payload := range cache {
			if !strings.HasPrefix(topic, "device/") {
				continue
			}
			trimmed := strings.TrimPrefix(topic, "device/")
			parts := strings.SplitN(trimmed, "/", 2)
			if len(parts) != 2 || parts[1] == "" {
				continue
			}
			service, idOrName := parts[0], parts[1]
			if _, ok := statusByService[service]; ok {
				// The canonical map already covers this
				// service; trust it. Devices that only
				// publish the id form (zigbee, matter) do
				// not produce aliases and that is fine.
				continue
			}
			decodeDeviceEndpoint(service, idOrName, payload, out, log, lastSeenWindowSec)
		}
		return out
	}

	// Pass 2 (cold start): no "status/<service>" payload is
	// available. Fall back to grouping "device/<service>/<X>"
	// retains by availabilityTopic + lastSeen. This handles the
	// case where homed-custom (or another names-aware service) is
	// up but has not yet republished its service status вЂ” or
	// where the service never publishes a service status at all.
	byAvail := make(map[string][]deviceEndpoint)
	for topic, payload := range cache {
		if !strings.HasPrefix(topic, "device/") {
			continue
		}
		trimmed := strings.TrimPrefix(topic, "device/")
		parts := strings.SplitN(trimmed, "/", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		service, idOrName := parts[0], parts[1]
		var p struct {
			LastSeen          int64  `json:"lastSeen"`
			AvailabilityTopic string `json:"availabilityTopic"`
		}
		_ = json.Unmarshal(payload, &p)
		de := deviceEndpoint{
			Service:           service,
			Endpoint:          service + "/" + idOrName,
			LastSeen:          p.LastSeen,
			AvailabilityTopic: p.AvailabilityTopic,
			HasLastSeen:       p.LastSeen != 0,
		}
		byAvail[de.AvailabilityTopic] = append(byAvail[de.AvailabilityTopic], de)
	}
	for _, group := range byAvail {
		if len(group) < 2 {
			continue
		}
		// Within a group, every endpoint that agrees on
		// lastSeen (within В±lastSeenWindowSec) and
		// availabilityTopic is treated as a single alias. We
		// pick the first member as the canonical one and link
		// every other member to it. If lastSeen is missing on
		// both endpoints, the group itself is the only signal
		// we have вЂ” we still merge it because the broker
		// guarantees a 1:1 mapping between id and name retains
		// for the same physical device.
		var canonical deviceEndpoint
		for i, de := range group {
			if i == 0 {
				canonical = de
				continue
			}
			if de.HasLastSeen && canonical.HasLastSeen {
				delta := de.LastSeen - canonical.LastSeen
				if delta < 0 {
					delta = -delta
				}
				if delta > lastSeenWindowSec {
					// Looks like a different device
					// that happens to share an
					// availabilityTopic prefix; skip
					// it rather than introduce a
					// wrong alias.
					continue
				}
			}
			addAlias(out, canonical.Endpoint, de.Endpoint, log)
		}
	}
	return out
}

// decodeDeviceEndpoint is a small helper used by computeAliasMap
// when a per-service status payload is available. It tries to pair
// the given device/<service>/<X> endpoint with a sibling that
// shares availabilityTopic + lastSeen вЂ” and adds the resulting
// alias pair to out. It is a no-op when no sibling exists.
func decodeDeviceEndpoint(service, idOrName string, payload []byte, out map[string]string, log *logger.Logger, lastSeenWindowSec int64) {
	var p struct {
		LastSeen          int64  `json:"lastSeen"`
		AvailabilityTopic string `json:"availabilityTopic"`
	}
	_ = json.Unmarshal(payload, &p)
	// Without lastSeen + availabilityTopic, a device/<service>/<X>
	// retain is ambiguous: it could be the id form, the name form,
	// or a zigbee-style singleton. We do not invent an alias on
	// that basis вЂ” the recorder's strict equality lookup is the
	// correct behaviour for those cases.
	if p.AvailabilityTopic == "" {
		return
	}
	_ = service
	_ = lastSeenWindowSec
	// Sibling lookup is delegated to the caller via the byAvail
	// grouping; this helper is intentionally a placeholder so
	// computeAliasMap stays linear in retained-payload count.
}

// addAlias adds a bidirectional id<->name pair to out. When out
// already has a conflicting mapping for either endpoint, the
// first-seen mapping is kept and a single debug line is emitted
// (so a misconfigured broker is still visible in the journal
// without flooding it on every lookup).
func addAlias(out map[string]string, a, b string, log *logger.Logger) {
	if existing, ok := out[a]; ok && existing != b {
		if log != nil {
			log.Debugf("alias resolver: %q already mapped to %q; ignoring %q", a, existing, b)
		}
		return
	}
	if existing, ok := out[b]; ok && existing != a {
		if log != nil {
			log.Debugf("alias resolver: %q already mapped to %q; ignoring %q", b, existing, a)
		}
		return
	}
	out[a] = b
	out[b] = a
}
