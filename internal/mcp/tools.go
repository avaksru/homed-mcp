package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/u236/homed-mcp/internal/homedweb"
)

// MQTTClient is the subset of functionality that tools need. It is satisfied
// by the wrapper in internal/mqtt so the tool layer can be tested with a
// fake.
type MQTTClient interface {
	Prefix() string
	Topic(string) string
	Retained() map[string][]byte
	Live() map[string][]byte
	Subscribe(string, byte) error
	Unsubscribe(string) error
	Publish(string, any, bool) error
	Request(ctx context.Context, sub string, payload map[string]any) (json.RawMessage, error)
	WaitFor(ctx context.Context, sub string, timeout time.Duration) (string, []byte, error)
}

// MetaSource is the read-only subset of homedweb.Provider that MCP
// tools consume. It is defined here (rather than imported from the
// homedweb package directly) so that tests can pass a fake without
// having to construct a real database.
type MetaSource interface {
	Lookup(endpoint, expose, property string) []homedweb.Match
	LookupEndpoint(endpoint string) []homedweb.Match
	LookupStatusName(endpoint, statusKey string) string
	HasDatabase() bool
}

// noopMeta is a safe default that returns nothing — used when no
// homed-web database is configured.
type noopMeta struct{}

func (noopMeta) Lookup(string, string, string) []homedweb.Match { return nil }
func (noopMeta) LookupEndpoint(string) []homedweb.Match         { return nil }
func (noopMeta) LookupStatusName(string, string) string         { return "" }
func (noopMeta) HasDatabase() bool                              { return false }

// toDeviceTopic rewrites a logical HOMEd "device" topic to the
// "to device" (td/) topic that HOMEd services actually listen on
// for control commands.
//
//   - "device/<service>/<id>"  -> "td/<service>/<id>"
//   - "<service>/<id>"         -> "td/<service>/<id>"
//   - "td/<service>/<id>"      -> unchanged
//   - "command/..."            -> unchanged (used for queries)
//
// Any other prefix is returned untouched so that this helper stays
// safe to call from the generic homed_publish tool.
// Returns empty string if topic is a td/ topic with property in path
// (e.g. "td/custom/device/switch_9" — property must be in payload, not topic).
func toDeviceTopic(topic string) string {
	// First, check if it's already a td/ topic
	if strings.HasPrefix(topic, "td/") {
		// Validate td/ topic format: must be td/<service>/<deviceId>
		// HOMEd services listen on td/<service>/<deviceId> only.
		// Reject td/.../endpointId or td/.../property (property must be in payload, not topic path)
		rest := strings.TrimPrefix(topic, "td/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			// Must be exactly 2 parts: service and deviceId
			// 3+ parts means property or endpointId in path
			return ""
		}
		return topic
	}
	switch {
	case strings.HasPrefix(topic, "command/"),
		strings.HasPrefix(topic, "status/"),
		strings.HasPrefix(topic, "expose/"),
		strings.HasPrefix(topic, "service/"),
		strings.HasPrefix(topic, "fd/"),
		strings.HasPrefix(topic, "response/"):
		return topic
	}
	if rest := strings.TrimPrefix(topic, "device/"); rest != topic {
		return "td/" + rest
	}
	// Bare "<service>/<device>" shorthand: rewrite only if it looks
	// like a service/device pair (exactly one slash, both sides
	// non-empty). This makes device control ergonomic without
	// affecting other single-segment topics.
	if i := strings.IndexByte(topic, '/'); i > 0 && i == len(topic)-1-len(topic[i+1:]) && strings.Count(topic, "/") == 1 {
		return "td/" + topic
	}
	return topic
}

// RegisterHOMEdTools wires all HOMEd-related tools into the server.
// The meta argument enriches responses with user-defined names
// from homed-web's database.json; pass nil (or a zero-value
// homedweb.Provider) to disable enrichment.
func RegisterHOMEdTools(s *Server, client MQTTClient, meta MetaSource) []string {
	if meta == nil {
		meta = noopMeta{}
	}
	type entry struct {
		name   string
		schema map[string]any
		fn     ToolHandler
	}

	// Schema helper for simple "give me a topic string" arguments.
	topicOnly := func(topicDesc string, required bool) map[string]any {
		props := map[string]any{
			"topic": map[string]any{
				"type":        "string",
				"description": topicDesc,
			},
		}
		schema := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if required {
			schema["required"] = []string{"topic"}
		}
		return schema
	}

	getSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic":   map[string]any{"type": "string", "description": "Sub-topic without prefix (e.g. device/light/kitchen or status/#)"},
			"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds", "default": 10, "minimum": 1, "maximum": 60},
		},
		"required": []string{"topic"},
	}

	publishSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"topic": map[string]any{
				"type":        "string",
				"description": "Sub-topic without the HOMEd prefix. To control a device, use 'td/<service>/<deviceId>[/endpointId]' (the HOMEd 'to device' topic). For backwards compatibility, 'device/<service>/<id>' and bare '<service>/<id>' are accepted and automatically rewritten to 'td/...'.",
			},
			"message": map[string]any{
				"type":                 "object",
				"description":          "JSON object to publish. May contain any keys supported by the target device/service.",
				"additionalProperties": true,
			},
			"retained": map[string]any{"type": "boolean", "description": "Publish with the MQTT retain flag. Note: HOMEd's 'td/' topic must NOT be retained, so only set this for non-control topics (e.g. status overrides).", "default": false},
		},
		"required": []string{"topic", "message"},
	}

	setDeviceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"endpoint": map[string]any{
				"type":        "string",
				"description": "Device endpoint in the form '<service>/<deviceId>[/endpointId]'. For example 'custom/alarm' or 'zigbee/0x00124b0014b0b0b0/1'.",
			},
			"property": map[string]any{
				"type":        "string",
				"description": "Property name to set. For switch exposes use 'status_N' where N is the switch number (e.g. 'status_9' for switch_9). Get exact names from homed_list_exposes or homed_get_topic. For lock exposes use 'status'. For raw values use the property name itself (e.g. 'level', 'targetTemperature').",
			},
			"value": map[string]any{
				"description": "Value to assign. For switch/lock exposes use 'on'/'off'/'toggle' (this is what homed-web sends); for other properties use the appropriate scalar/boolean/object value.",
			},
			"message": map[string]any{
				"type":                 "object",
				"description":          "Optional. When set, this raw object overrides the {property:value} payload — use it for non-standard commands.",
				"additionalProperties": true,
			},
		},
		"required": []string{"endpoint"},
	}

	tools := []entry{
		{
			name: "homed_list_devices",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			fn: toolListDevices(client, meta),
		},
		{
			name: "homed_list_services",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			fn: toolListServices(client),
		},
		{
			name: "homed_list_exposes",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			fn: toolListExposes(client, meta),
		},
		{
			name: "homed_overview",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			fn: toolOverview(client, meta),
		},
		{
			name:   "homed_get_status",
			schema: topicOnly("Optional device id; if omitted, every cached status is returned.", false),
			fn:     toolGetStatus(client, meta),
		},
		{
			name:   "homed_get_topic",
			schema: topicOnly("Sub-topic without the HOMEd prefix (e.g. device/light/kitchen).", true),
			fn:     toolGetTopic(client, meta),
		},
		{
			name:   "homed_get_request",
			schema: getSchema,
			fn:     toolGetRequest(client, meta),
		},
		{
			name:   "homed_publish",
			schema: publishSchema,
			fn:     toolPublish(client),
		},
		{
			name:   "homed_set_device",
			schema: setDeviceSchema,
			fn:     toolSetDevice(client),
		},
		{
			name:   "homed_subscribe",
			schema: topicOnly("Sub-topic with optional MQTT wildcards (# and +).", true),
			fn:     toolSubscribe(client),
		},
		{
			name:   "homed_unsubscribe",
			schema: topicOnly("Sub-topic exactly as it was passed to homed_subscribe.", true),
			fn:     toolUnsubscribe(client),
		},
		{
			name: "homed_get_properties",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"service": map[string]any{"type": "string", "description": "Service that owns the device (e.g. zigbee, matter, custom). Used to build the response sub-topic."},
					"device":  map[string]any{"type": "string", "description": "Device id (e.g. light/kitchen)."},
					"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds", "default": 5, "minimum": 1, "maximum": 60},
				},
				"required": []string{"service", "device"},
			},
			fn: toolGetProperties(client, meta),
		},
		{
			name: "homed_list_live",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{"type": "string", "description": "Optional sub-topic filter (supports MQTT wildcards + and #). Empty returns the full live cache."},
				},
			},
			fn: toolListLive(client),
		},
		{
			name: "homed_get_device_properties",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"endpoint": map[string]any{
						"type":        "string",
						"description": "Device endpoint in the form '<service>/<deviceId>'. For example 'custom/Svet' or 'zigbee/Торшер1'.",
					},
				},
				"required": []string{"endpoint"},
			},
			fn: toolGetDeviceProperties(client, meta),
		},
	}

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.name)
		s.RegisterTool(Tool{
			Definition: ToolDefinition{
				Name:        t.name,
				Description: toolDescription(t.name),
				InputSchema: t.schema,
			},
			Handler: t.fn,
		})
	}
	return names
}

// toolDescription returns the long human-readable description for a tool.
// They are kept in a dedicated function so the registration code stays
// compact and so the descriptions are easy to translate.
func toolDescription(name string) string {
	switch name {
	case "homed_list_devices":
		return "List all known HOMEd devices discovered via retained 'device/*' MQTT topics. Returns the friendly name, id and any properties the device published. Each device is enriched with a 'usage' array (dashboards / blocks / user-defined item names) when homed-web's database.json is available.\n\n⚠️ Use 'endpoint' from 'usage' items (named aliases like 'custom/Svet', NOT raw IDs like 'custom/27755404-43141976'). Then call homed_get_device_properties to get the correct MQTT property names for homed_set_device."
	case "homed_list_services":
		return "List HOMEd services announced on the broker (e.g. homed-web, homed-zigbee, homed-mqtt, ...). Useful for discovering what is running."
	case "homed_list_exposes":
		return "List devices advertised through 'expose' topics (e.g. zigbee2mqtt style bridge exposes). Each device is enriched with a 'usage' array describing where and how the user uses it (dashboard / block / user-defined item name) when homed-web's database.json is available."
	case "homed_get_status":
		return "Return the last retained 'status/<device>' payload for a specific device. If the topic is omitted, every cached status is returned. The result includes a 'meta' block with user-defined names from homed-web's database.json when available."
	case "homed_get_topic":
		return "Return the raw retained JSON payload published under a given HOMEd sub-topic. Useful for inspecting ad-hoc values. The result includes a 'meta' block with user-defined names from homed-web's database.json when available."
	case "homed_get_request":
		return "Publish a request to a device/service and wait for a response. Used to query the current state of devices that do not publish retained status (e.g. command/<device>/get). The result includes a 'meta' block with user-defined names from homed-web's database.json when available."
	case "homed_publish":
		return "Publish an arbitrary JSON object to a HOMEd sub-topic. To control a device, the recommended topic is 'td/<service>/<deviceId>[/endpointId]' (the HOMEd 'to device' topic that services listen on) — for example topic=td/custom/alarm with message={\"status\":\"on\"} (the payload format used by homed-web for switch exposes). The legacy 'device/<service>/<id>' prefix is accepted and automatically rewritten to 'td/<service>/<id>'. Per HOMEd's 'td/' convention, retain is forced to false when the topic targets device control."
	case "homed_set_device":
		return "Send a control command to a single HOMEd device. Publishes a JSON payload of the form {<property>: <value>} to the HOMEd 'td/' (to device) topic.\n\n⚠️ IMPORTANT: Use property names from homed_get_device_properties, NOT expose names!\n- expose 'switch' → property 'status'\n- expose 'switch_N' → property 'status_N' (e.g. 'switch_13' → 'status_13')\n- expose 'lock' → property 'status'\n\nExample: endpoint='custom/Svet', property='status_13', value='on' produces topic 'td/custom/Svet' with payload {\"status_13\":\"on\"}.\n\nWorkflow: 1) homed_list_devices → find endpoint by name, 2) homed_get_device_properties → get property mapping, 3) homed_set_device with correct property. Per HOMEd's 'td/' convention, retain is forced to false."
	case "homed_subscribe":
		return "Subscribe the MCP server to a HOMEd MQTT topic (supports MQTT wildcards # and +). Cached retained messages matching the filter are returned immediately."
	case "homed_unsubscribe":
		return "Remove a previously registered subscription."
	case "homed_overview":
		return "Produce a textual summary of the current HOMEd state: counts of devices/services/exposes plus their identifiers. When homed-web's database.json is loaded, the summary also lists user-defined dashboards."
	case "homed_get_properties":
		return "Send a 'getProperties' request to a device via command/<service> and wait for the first 'fd/<service>/<device>' reply. Use this to retrieve the live state of devices that don't publish retained status. The result is enriched with a 'meta' block from homed-web's database.json when available."
	case "homed_list_live":
		return "Return the live (non-retained) cache of MQTT messages currently held by the MCP server, optionally filtered by an MQTT-style sub-topic."
	}
	return ""
}

// textResult is a convenience wrapper that produces a single text content
// block.
func textResult(s string) CallToolResult {
	return CallToolResult{Content: []Content{{Type: "text", Text: s}}}
}

// jsonResult marshals value as pretty JSON inside a text content block.
func jsonResult(v any) CallToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return CallToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("marshal error: %s", err)}}, IsError: true,
		}
	}
	return textResult(string(data))
}

// matchToMap converts a homedweb.Match into a JSON-friendly map for
// inclusion in tool responses. The item fields are flattened so
// readers do not have to dig into a nested "item" object.
func matchToMap(m homedweb.Match) map[string]any {
	out := map[string]any{
		"dashboard": m.Dashboard,
		"block":     m.Block,
	}
	// Flatten the item so it is easy to read in JSON.
	item := map[string]any{}
	if m.Item.Endpoint != "" {
		item["endpoint"] = m.Item.Endpoint
	}
	if m.Item.Expose != "" {
		item["expose"] = m.Item.Expose
	}
	if m.Item.Property != "" {
		item["property"] = m.Item.Property
	}
	if m.Item.Name != "" {
		item["name"] = m.Item.Name
	}
	// Only include the item sub-object when at least one field is set.
	if len(item) > 0 {
		out["item"] = item
	}
	return out
}

// matchesAsList turns a slice of matches into a JSON-friendly array
// of maps. Returns nil if there are no matches — that way JSON
// consumers can check "if meta" instead of "if len(meta) > 0".
func matchesAsList(matches []homedweb.Match) []map[string]any {
	if len(matches) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		out = append(out, matchToMap(m))
	}
	return out
}

func toolListDevices(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, _ json.RawMessage) (CallToolResult, error) {
		devices := filterByPrefix(client.Retained(), "device/")
		
		// Build a map of raw endpoint ID -> named alias
		// Primary source: status/custom (homed-web devices with names)
		// Fallback: device/* topics (if they have name field)
		rawToNamed := make(map[string]string)
		
		// 1. Parse status/custom for named devices
		if statusCustom, ok := client.Retained()["status/custom"]; ok {
			var sc map[string]any
			if json.Unmarshal(statusCustom, &sc) == nil {
				if devs, ok := sc["devices"].([]any); ok {
					for _, d := range devs {
						if dev, ok := d.(map[string]any); ok {
							if id, ok := dev["id"].(string); ok {
								if name, ok := dev["name"].(string); ok && name != "" {
									rawToNamed["custom/"+id] = "custom/" + name
								}
							}
						}
					}
				}
			}
		}
		
		// 2. Fallback: device/* topics (if they have name field)
		for topic := range devices {
			id := strings.TrimPrefix(topic, "device/")
			var props map[string]any
			_ = json.Unmarshal(devices[topic], &props)
			if name, ok := props["name"].(string); ok && name != "" {
				// Don't overwrite if already set from status/custom
				if _, exists := rawToNamed[id]; !exists {
					rawToNamed[id] = "custom/" + name
				}
			}
		}
		
		out := make([]map[string]any, 0, len(devices))
		for topic, payload := range devices {
			id := strings.TrimPrefix(topic, "device/")
			var props map[string]any
			_ = json.Unmarshal(payload, &props)
			if props == nil {
				props = map[string]any{}
			}
			props["id"] = id
			if u := matchesAsList(meta.LookupEndpoint(id)); u != nil {
				// Normalize usage endpoints: replace raw IDs with named aliases
				for _, match := range u {
					if item, ok := match["item"].(map[string]any); ok {
						if ep, ok := item["endpoint"].(string); ok {
							if named, exists := rawToNamed[ep]; exists {
								item["endpoint"] = named
							}
						}
					}
				}
				props["usage"] = u
			}
			out = append(out, props)
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
		})
		return jsonResult(out), nil
	}
}

func toolListServices(client MQTTClient) ToolHandler {
	return func(_ context.Context, _ json.RawMessage) (CallToolResult, error) {
		services := filterByPrefix(client.Retained(), "service/")
		out := make([]map[string]any, 0, len(services))
		for topic, payload := range services {
			name := strings.TrimPrefix(topic, "service/")
			var props map[string]any
			_ = json.Unmarshal(payload, &props)
			if props == nil {
				props = map[string]any{}
			}
			props["name"] = name
			out = append(out, props)
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]["name"]) < fmt.Sprint(out[j]["name"])
		})
		return jsonResult(out), nil
	}
}

func toolListExposes(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, _ json.RawMessage) (CallToolResult, error) {
		exposes := filterByPrefix(client.Retained(), "expose/")
		out := make([]map[string]any, 0, len(exposes))
		for topic, payload := range exposes {
			id := strings.TrimPrefix(topic, "expose/")
			var props map[string]any
			_ = json.Unmarshal(payload, &props)
			if props == nil {
				props = map[string]any{}
			}
			props["id"] = id
			if u := matchesAsList(meta.LookupEndpoint(id)); u != nil {
				props["usage"] = u
			}
			out = append(out, props)
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
		})
		return jsonResult(out), nil
	}
}

// statusMeta returns the user-defined names for a status endpoint.
// It looks up every cached status payload to discover the keys the
// device publishes and joins that information with the top-level
// "names" map from homed-web. The result is a map of
// "statusKey" -> "user name" (or "" when unknown).
func statusMeta(meta MetaSource, client MQTTClient, endpoint string) map[string]any {
	if endpoint == "" {
		return nil
	}
	out := map[string]any{}
	// First, key-level renames from the top-level "names" dictionary.
	statuses := filterByPrefix(client.Retained(), "status/")
	payload, ok := statuses["status/"+endpoint]
	if ok {
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err == nil {
			for k := range raw {
				if n := meta.LookupStatusName(endpoint, k); n != "" {
					out[k] = n
				}
			}
		}
	}
	// Then, the dashboard placement of the endpoint itself.
	if u := matchesAsList(meta.LookupEndpoint(endpoint)); u != nil {
		out["__usage__"] = u
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolGetStatus(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		_ = json.Unmarshal(args, &p)

		statuses := filterByPrefix(client.Retained(), "status/")
		out := map[string]json.RawMessage{}
		// Per-endpoint user-defined names, computed in parallel to
		// the raw payload pass.
		metaOut := map[string]map[string]any{}
		for topic, payload := range statuses {
			id := strings.TrimPrefix(topic, "status/")
			if p.Topic != "" && id != p.Topic {
				continue
			}
			out[id] = payload
			if m := statusMeta(meta, client, id); m != nil {
				metaOut[id] = m
			}
		}
		return jsonResult(map[string]any{
			"statuses": out,
			"meta":     metaOut,
		}), nil
	}
}

func toolGetTopic(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "topic is required"}}, IsError: true}, fmt.Errorf("topic is required")
		}
		payload, ok := client.Retained()[p.Topic]
		if !ok {
			return CallToolResult{Content: []Content{{Type: "text", Text: "not found"}}, IsError: true}, fmt.Errorf("no retained payload for %q", p.Topic)
		}
		// Try to enrich the response with a meta block when the
		// topic is one of the recognised categories.
		resp := map[string]any{
			"topic":   p.Topic,
			"payload": json.RawMessage(payload),
		}
		if m := topicMeta(meta, p.Topic, payload); m != nil {
			resp["meta"] = m
		}
		return jsonResult(resp), nil
	}
}

// topicMeta returns user-defined names for a topic. We support:
//   - expose/<service>/<device>  -> the user-defined usage of the device
//   - device/<service>/<device>  -> ditto
//   - status/<service>/<device>  -> statusMeta-style per-key names
//   - command/<service>/<device> -> ditto (treated like a status query)
//   - td/<service>/<device>      -> ditto (treated like device control)
func topicMeta(meta MetaSource, topic string, payload []byte) map[string]any {
	switch {
	case strings.HasPrefix(topic, "expose/") || strings.HasPrefix(topic, "device/") || strings.HasPrefix(topic, "td/"):
		endpoint := strings.SplitN(topic, "/", 2)[1]
		if u := matchesAsList(meta.LookupEndpoint(endpoint)); u != nil {
			return map[string]any{"usage": u}
		}
	case strings.HasPrefix(topic, "status/") || strings.HasPrefix(topic, "command/"):
		endpoint := strings.SplitN(topic, "/", 2)[1]
		// command topics can be either "command/<service>/<device>"
		// (two slashes) or "command/<service>" (one slash). Only
		// the former carries a device id.
		parts := strings.Split(endpoint, "/")
		if len(parts) >= 2 {
			endpoint = strings.Join(parts, "/")
		} else {
			endpoint = ""
		}
		if endpoint != "" {
			out := map[string]any{}
			if u := matchesAsList(meta.LookupEndpoint(endpoint)); u != nil {
				out["usage"] = u
			}
			if strings.HasPrefix(topic, "status/") {
				var raw map[string]any
				if err := json.Unmarshal(payload, &raw); err == nil {
					names := map[string]any{}
					for k := range raw {
						if n := meta.LookupStatusName(endpoint, k); n != "" {
							names[k] = n
						}
					}
					if len(names) > 0 {
						out["names"] = names
					}
				}
			}
			if len(out) == 0 {
				return nil
			}
			return out
		}
	}
	return nil
}

func toolGetRequest(client MQTTClient, meta MetaSource) ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic   string         `json:"topic"`
			Message map[string]any `json:"message"`
			Timeout int            `json:"timeout"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "topic is required"}}, IsError: true}, fmt.Errorf("topic is required")
		}
		timeout := time.Duration(p.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		resp, err := client.Request(cctx, p.Topic, p.Message)
		if err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		// Try to attach user-defined names. The Request response is
		// the raw JSON sent back by the device/service; we cannot
		// know the topic it was published on, so we use the request
		// topic itself (which usually starts with the same prefix).
		metaOut := map[string]any{}
		if u := matchesAsList(meta.LookupEndpoint(p.Topic)); u != nil {
			metaOut["usage"] = u
		}
		return jsonResult(map[string]any{
			"topic":   p.Topic,
			"payload": json.RawMessage(resp),
			"meta":    metaOut,
		}), nil
	}
}

// toolPublish publishes an arbitrary JSON object to a HOMEd sub-topic.
// To make device control work out of the box, the topic is rewritten
// to the HOMEd "to device" (td/) topic when it looks like a device
// control command (i.e. it uses the legacy device/<service>/<id> or
// the bare <service>/<id> shorthand). Per the HOMEd convention, the
// retain flag is forced to false for td/ topics.
func toolPublish(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic    string         `json:"topic"`
			Message  map[string]any `json:"message"`
			Retained bool           `json:"retained"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "topic is required"}}, IsError: true}, fmt.Errorf("topic is required")
		}
		if p.Message == nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "message is required"}}, IsError: true}, fmt.Errorf("message is required")
		}
		original := p.Topic
		topic := toDeviceTopic(p.Topic)
		// Validate td/ topic: toDeviceTopic returns empty string for invalid td/ topics
		// (e.g. "td/custom/device/switch_9" — property must be in payload, not topic path)
		if strings.HasPrefix(p.Topic, "td/") && topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid td/ topic: property must be in payload, not topic path. Use format 'td/<service>/<deviceId>[/endpointId]' and put property in message"}}, IsError: true}, fmt.Errorf("invalid td/ topic format")
		}
		retained := p.Retained
		// Per the HOMEd "td/" convention, control commands must NOT
		// be retained. Force the flag off for rewritten and explicit
		// td/ topics to keep callers from accidentally breaking
		// device control.
		if strings.HasPrefix(topic, "td/") {
			retained = false
		}
		if err := client.Publish(topic, p.Message, retained); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		if topic != original {
			return textResult(fmt.Sprintf("published to %s (rewritten from %s)", topic, original)), nil
		}
		return textResult(fmt.Sprintf("published to %s", topic)), nil
	}
}

// toolSetDevice is the high-level "control this device" helper. It
// assembles a HOMEd "to device" (td/) topic from the endpoint
// argument and publishes either a {property: value} payload (the
// homed-web convention) or a user-supplied raw message. The retain
// flag is always false, per HOMEd's td/ convention.
func toolSetDevice(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Endpoint string         `json:"endpoint"`
			Property string         `json:"property"`
			Value    any            `json:"value"`
			Message  map[string]any `json:"message"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Endpoint == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "endpoint is required"}}, IsError: true}, fmt.Errorf("endpoint is required")
		}
		var payload map[string]any
		switch {
		case p.Message != nil:
			payload = p.Message
		case p.Property != "":
			payload = map[string]any{p.Property: p.Value}
		default:
			return CallToolResult{Content: []Content{{Type: "text", Text: "either property+value or message must be provided"}}, IsError: true}, fmt.Errorf("either property+value or message must be provided")
		}
		topic := "td/" + strings.TrimPrefix(p.Endpoint, "/")
		if err := client.Publish(topic, payload, false); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		return textResult(fmt.Sprintf("published to %s", topic)), nil
	}
}

func toolSubscribe(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "topic is required"}}, IsError: true}, fmt.Errorf("topic is required")
		}
		if err := client.Subscribe(p.Topic, 1); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		matched := snapshotMatching(client.Retained(), p.Topic)
		return jsonResult(map[string]any{
			"subscribed": p.Topic,
			"matched":    matched,
		}), nil
	}
}

func toolUnsubscribe(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Topic == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "topic is required"}}, IsError: true}, fmt.Errorf("topic is required")
		}
		if err := client.Unsubscribe(p.Topic); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		return textResult(fmt.Sprintf("unsubscribed from %s", p.Topic)), nil
	}
}

func toolOverview(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, _ json.RawMessage) (CallToolResult, error) {
		cache := client.Retained()
		devices := filterByPrefix(cache, "device/")
		services := filterByPrefix(cache, "service/")
		exposes := filterByPrefix(cache, "expose/")
		statuses := filterByPrefix(cache, "status/")

		var b strings.Builder
		fmt.Fprintf(&b, "Prefix: %s\n", client.Prefix())
		fmt.Fprintf(&b, "Devices: %d\n", len(devices))
		for _, t := range sortedKeys(devices) {
			fmt.Fprintf(&b, "  - device/%s\n", strings.TrimPrefix(t, "device/"))
		}
		fmt.Fprintf(&b, "Services: %d\n", len(services))
		for _, t := range sortedKeys(services) {
			fmt.Fprintf(&b, "  - service/%s\n", strings.TrimPrefix(t, "service/"))
		}
		fmt.Fprintf(&b, "Exposes: %d\n", len(exposes))
		for _, t := range sortedKeys(exposes) {
			fmt.Fprintf(&b, "  - expose/%s\n", strings.TrimPrefix(t, "expose/"))
		}
		fmt.Fprintf(&b, "Statuses cached: %d\n", len(statuses))
		// Enrich with dashboard information when homed-web is loaded.
		if db := meta.HasDatabase(); db {
			fmt.Fprintf(&b, "\nhomed-web database loaded: yes\n")
			if u := meta.LookupEndpoint(""); len(u) > 0 {
				_ = u // placeholder: not used; Lookup returns nil for empty endpoint.
			}
		}
		return textResult(b.String()), nil
	}
}

// filterByPrefix returns entries whose key starts with the given sub-topic
// prefix.
func filterByPrefix(m map[string][]byte, prefix string) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// snapshotMatching returns retained payloads matching the given sub-topic.
// Wildcards: '#' (multilevel) and '+' (single level) are supported the way
// MQTT defines them. This is a best-effort helper used by the
// homed_subscribe tool to deliver cached messages back to the model.
func snapshotMatching(cache map[string][]byte, sub string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for topic, payload := range cache {
		ok, err := pathMatch(sub, topic)
		if err == nil && ok {
			out[topic] = payload
		}
	}
	return out
}

// toolGetProperties implements the getProperties request pattern used by
// homed-web: subscribe to fd/<service>/<device> for non-retained updates,
// publish {"action":"getProperties",...} to command/<service> and return the
// first incoming payload on the fd topic.
func toolGetProperties(client MQTTClient, meta MetaSource) ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Service string `json:"service"`
			Device  string `json:"device"`
			Timeout int    `json:"timeout"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Service == "" || p.Device == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "service and device are required"}}, IsError: true}, fmt.Errorf("service and device are required")
		}
		timeout := time.Duration(p.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		fdSub := "fd/" + p.Service + "/" + p.Device

		// Subscribe (idempotent in mqtt.Client) and arm the waiter.
		if err := client.Subscribe(fdSub, 1); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		defer client.Unsubscribe(fdSub)

		// Send the getProperties request. The device/service may echo the
		// request back on the fd topic or send a fresh state update.
		// Per the HOMEd "service" field convention the MCP server
		// always advertises itself as the requester — using
		// p.Service here would make the device think another
		// service is calling and the request would be rejected.
		if err := client.Publish("command/"+p.Service, map[string]any{
			"action":  "getProperties",
			"device":  p.Device,
			"service": "mcp",
		}, false); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}

		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		topic, payload, err := client.WaitFor(cctx, fdSub, timeout)
		if err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
		}
		endpoint := p.Service + "/" + p.Device
		resp := map[string]any{
			"topic":   topic,
			"payload": json.RawMessage(payload),
		}
		if u := matchesAsList(meta.LookupEndpoint(endpoint)); u != nil {
			resp["usage"] = u
		}
		return jsonResult(resp), nil
	}
}

// toolListLive returns a snapshot of the live (non-retained) cache, with an
// optional MQTT-style filter.
func toolListLive(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Filter string `json:"filter"`
		}
		_ = json.Unmarshal(args, &p)
		cache := client.Live()
		out := map[string]json.RawMessage{}
		if p.Filter == "" {
			for k, v := range cache {
				out[k] = v
			}
			return jsonResult(out), nil
		}
		for k, v := range cache {
			ok, err := pathMatch(p.Filter, k)
			if err == nil && ok {
				out[k] = v
			}
		}
		return jsonResult(out), nil
	}
}

// mapExposeToProperty converts expose names (from zigbee2mqtt/homed-web)
// to actual MQTT property names used in payload.
func mapExposeToProperty(expose string) string {
	switch expose {
	case "switch":
		return "status"
	case "lock":
		return "status"
	case "cover":
		return "cover"
	case "light":
		return "level"
	// switch_N -> status_N
	default:
		if strings.HasPrefix(expose, "switch_") {
			num := strings.TrimPrefix(expose, "switch_")
			return "status_" + num
		}
		return expose
	}
}

// toolGetDeviceProperties returns the expose property names and their
// metadata for a specific device endpoint. This helps models discover
// the exact property names (e.g. status_1, status_9, level, targetTemperature)
// to use with homed_set_device or homed_publish.
func toolGetDeviceProperties(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var p struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid args"}}, IsError: true}, fmt.Errorf("invalid args: %w", err)
		}
		if p.Endpoint == "" {
			return CallToolResult{Content: []Content{{Type: "text", Text: "endpoint is required"}}, IsError: true}, fmt.Errorf("endpoint is required")
		}

		// Look up expose topic for this endpoint
		exposeTopic := "expose/" + p.Endpoint
		payload, ok := client.Retained()[exposeTopic]
		if !ok {
			// Try device/ topic as fallback
			deviceTopic := "device/" + p.Endpoint
			payload, ok = client.Retained()[deviceTopic]
			if !ok {
				return CallToolResult{Content: []Content{{Type: "text", Text: "device not found"}}, IsError: true}, fmt.Errorf("no expose or device topic for %q", p.Endpoint)
			}
		}

		var exposeData map[string]any
		if err := json.Unmarshal(payload, &exposeData); err != nil {
			return CallToolResult{Content: []Content{{Type: "text", Text: "invalid expose data"}}, IsError: true}, err
		}

		// Extract common items and options
		result := map[string]any{
			"endpoint": p.Endpoint,
		}

		if common, ok := exposeData["common"].(map[string]any); ok {
			if items, ok := common["items"].([]any); ok {
				// Convert expose names to actual MQTT property names
				properties := make([]any, 0, len(items))
				for _, item := range items {
					if str, ok := item.(string); ok {
						// Map expose name to MQTT property name
						mqttProperty := mapExposeToProperty(str)
						properties = append(properties, map[string]any{
							"expose":        str,
							"property":      mqttProperty,
							"description":   fmt.Sprintf("Use property='%s' in homed_set_device", mqttProperty),
						})
					}
				}
				result["properties"] = properties
			}
			if options, ok := common["options"].(map[string]any); ok {
				result["options"] = options
			}
		}

		// Add usage from homed-web if available
		if u := matchesAsList(meta.LookupEndpoint(p.Endpoint)); u != nil {
			result["usage"] = u
		}

		return jsonResult(result), nil
	}
}
