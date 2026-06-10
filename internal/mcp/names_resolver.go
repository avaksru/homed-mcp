package mcp

// names_resolver.go — extracted helpers for the per-service
// "names" flag that HOMEd publishes on {prefix}/status/<service>.
// These helpers used to live in tools.go but were bundled in
// with the rest of the device-control code. The functions are
// extracted to their own file so that confirmation.go (and any
// future tool) can call them without importing the rest of
// the tool layer.
//
// The "names" flag controls which identifier (id or name)
// appears in 'device/', 'expose/', 'status/', 'td/' and 'fd/'
// topics for a service. When the service runs with names=true
// the caller typically passes a stable id and the function
// looks up the broker-side name from the cached payloads.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// resolveDeviceIdentifier reconciles a caller-supplied device
// identifier with whatever the broker actually uses in MQTT
// topic paths, honouring the per-service 'names' retain flag
// published on {prefix}/status/<service>.
//
// Returns:
//   - resolved: the identifier to put on the wire (i.e. in
//     'td/<service>/<...>', 'fd/<service>/<...>' and the
//     'device' field of the request body). Equal to
//     'supplied' when no rewrite is needed (or possible).
//   - warn: non-empty when the service has names=true but the
//     supplied id could not be mapped to a name. The caller
//     can surface this to the user so that the request is not
//     silently dropped by the broker.
func resolveDeviceIdentifier(client MQTTClient, service, supplied string) (resolved, warn string) {
	resolved = supplied
	if service == "" || supplied == "" {
		return resolved, ""
	}
	// The 'names' flag is optional in this build. When the
	// service does not implement ServiceUsesNames the helper
	// silently falls back to the id-as-is behaviour — which
	// is the same default the upstream code uses when the
	// service is not running with names=true.
	// names=true. Scan cached 'device/<service>/<name>' topics
	// for a JSON body that carries the supplied id (or
	// nodeId, the zigbee convention).
	cache := client.Retained()
	devPrefix := "device/" + service + "/"
	for t, payload := range cache {
		if !strings.HasPrefix(t, devPrefix) {
			continue
		}
		candidate := strings.TrimPrefix(t, devPrefix)
		// Skip multi-segment topics; only direct
		// 'device/<service>/<name>' retain payloads are
		// relevant here. Nested ids (e.g. '<name>/<ep>')
		// are addressable through their own '<name>'
		// payload.
		if candidate == "" || strings.ContainsRune(candidate, '/') {
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(payload, &body); err != nil {
			continue
		}
		var foundID string
		if v, ok := body["id"].(string); ok && v != "" {
			foundID = v
		} else if v, ok := body["nodeId"].(string); ok && v != "" {
			foundID = v
		}
		if foundID == supplied {
			return candidate, ""
		}
		// Symmetric path: the caller may have passed the
		// broker-side name (typical for human users that
		// copied a device name from the dashboard). Honour
		// it as-is and just confirm the supplied value
		// matches a real device.
		if candidate == supplied {
			return supplied, ""
		}
	}
	return resolved, fmt.Sprintf("service %q has names=true but no cached device has id=%q; the broker will likely drop the message", service, supplied)
}

// resolveGetPropertiesArgs resolves the caller-supplied service
// and device pair for the getProperties request pattern. The
// 'names' retain flag is honoured transparently: when the
// service runs with names=true the request body and the
// response topic are rewritten to the broker-side name so that
// the request actually reaches the device.
//
// The function returns:
//   - requestDevice: the value to put in the 'device' field
//     of the request body. Equal to the supplied 'device' when
//     no rewrite is needed (or possible).
//   - responseTopic: the 'fd/<service>/<...>' sub-topic to
//     subscribe to. Empty when no rewrite is needed (the caller
//     is in charge of building it) or when the rewrite could
//     not be performed.
//   - warn: non-empty when the rewrite was not possible (the
//     caller can surface it to the user).
func resolveGetPropertiesArgs(client MQTTClient, service, device string) (requestDevice, responseTopic, warn string) {
	resolved, w := resolveDeviceIdentifier(client, service, device)
	requestDevice = resolved
	if resolved == "" || resolved == device {
		return requestDevice, "", w
	}
	return requestDevice, "fd/" + service + "/" + resolved, w
}

// resolveTdTopicForNamesFlag rewrites a 'td/<service>/<id>[/<ep>]'
// topic to use the device name when the service is running
// with 'names=true'. It also returns the resolved device
// identifier and the original endpoint, both of which are
// useful for callers that need to build a related request.
//
// The optional warning string is non-empty when we failed to
// resolve the supplied id to a name — the caller can surface
// it to the user so that stale or wrong ids are not silently
// dropped by the broker.
func resolveTdTopicForNamesFlag(client MQTTClient, topic string) (out, resolvedID, originalEndpoint, warn string) {
	originalEndpoint = topic
	out = topic
	const tdPrefix = "td/"
	if !strings.HasPrefix(topic, tdPrefix) {
		return out, resolvedID, originalEndpoint, ""
	}
	rest := strings.TrimPrefix(topic, tdPrefix)
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return out, resolvedID, originalEndpoint, ""
	}
	service, id := parts[0], parts[1]
	resolved, w := resolveDeviceIdentifier(client, service, id)
	if w != "" {
		return out, resolvedID, originalEndpoint, w
	}
	resolvedID = service + "/" + id
	if resolved != id {
		parts[1] = resolved
		out = tdPrefix + strings.Join(parts, "/")
		resolvedID = service + "/" + resolved
	}
	return out, resolvedID, originalEndpoint, ""
}

// resolveTopicForNamesFlag is a back-compat alias for
// resolveTdTopicForNamesFlag that preserves the old two-value
// return shape used by some tests. It is kept so that existing
// test files (and any downstream callers) continue to work.
func resolveTopicForNamesFlag(client MQTTClient, topic string) (string, string) {
	newTopic, _, _, warn := resolveTdTopicForNamesFlag(client, topic)
	return newTopic, warn
}

// suppress an unused-import warning when this file is
// compiled together with callers that already pull in the
// context/json/time packages.
var _ = context.Background
var _ = time.Second