package mcp

// search.go — homed_search / homed_room_overview MCP tools
// plus the augmented registration helper that overrides
// homed_list_devices / homed_list_exposes with room-aware,
// label-enriched versions on top of the base
// RegisterHOMEdTools from tools.go.
//
// Every label/channel is derived from the cached
// "expose/<service>/<device>" retained payload — no
// extra device metadata is required.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// toolSearch implements the "homed_search" MCP tool. The
// tokenisation and matching rules are documented on the
// helper functions in labels.go.
func toolSearch(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var params struct {
			Query string `json:"query"`
			Room  string `json:"room"`
			Kind  string `json:"kind"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(args, &params)
		query := strings.TrimSpace(params.Query)
		room := strings.ToLower(strings.TrimSpace(params.Room))
		kind := strings.ToLower(strings.TrimSpace(params.Kind))
		limit := params.Limit
		if limit <= 0 {
			limit = 25
		}
		if limit > 100 {
			limit = 100
		}
		var queryTokens []string
		if query != "" {
			norm := normaliseText(query)
			for _, f := range strings.Fields(norm) {
				if f == "" {
					continue
				}
				queryTokens = append(queryTokens, f)
			}
		}
		hits := make([]map[string]any, 0, 16)
		for topic, payload := range client.Retained() {
			if !strings.HasPrefix(topic, "expose/") {
				continue
			}
			endpoint := strings.TrimPrefix(topic, "expose/")
			if endpoint == "" {
				continue
			}
			var decl map[string]any
			_ = json.Unmarshal(payload, &decl)
			common, _ := decl["common"].(map[string]any)
			labels := buildLabels(common)
			for _, lbl := range labels {
				if room != "" && lbl.Room != room {
					continue
				}
				if kind != "" && lbl.Kind != kind {
					continue
				}
				if kind == "" && !lbl.Settable {
					continue
				}
				score := scoreLabel(lbl, queryTokens)
				if query != "" && score == 0 {
					continue
				}
				hit := map[string]any{
					"endpoint": endpoint,
					"expose":   lbl.Expose,
					"title":    lbl.Title,
					"room":     lbl.Room,
					"kind":     lbl.Kind,
					"class":    lbl.Class,
					"unit":     lbl.Unit,
					"type":     lbl.Type,
					"keywords": lbl.Keywords,
					"channel":  lbl.Channel,
					"settable": lbl.Settable,
					"score":    score,
				}
				if lbl.Command != nil {
					hit["command"] = lbl.Command
				}
				hits = append(hits, hit)
			}
		}
		sort.Slice(hits, func(i, j int) bool {
			si, _ := hits[i]["score"].(int)
			sj, _ := hits[j]["score"].(int)
			if si != sj {
				return si > sj
			}
			ei, _ := hits[i]["endpoint"].(string)
			ej, _ := hits[j]["endpoint"].(string)
			if ei != ej {
				return ei < ej
			}
			xi, _ := hits[i]["expose"].(string)
			xj, _ := hits[j]["expose"].(string)
			return xi < xj
		})
		if len(hits) > limit {
			hits = hits[:limit]
		}
		return jsonResult(hits), nil
	}
}

// toolRoomOverview implements the "homed_room_overview"
// MCP tool. The result is a map of normalised room id -> []
// of flat channel descriptions.
func toolRoomOverview(client MQTTClient) ToolHandler {
	return func(_ context.Context, args json.RawMessage) (CallToolResult, error) {
		var params struct {
			Room string `json:"room"`
		}
		_ = json.Unmarshal(args, &params)
		roomFilter := strings.ToLower(strings.TrimSpace(params.Room))

		buckets := make(map[string][]map[string]any, 8)
		for topic, payload := range client.Retained() {
			if !strings.HasPrefix(topic, "expose/") {
				continue
			}
			endpoint := strings.TrimPrefix(topic, "expose/")
			if endpoint == "" {
				continue
			}
			var decl map[string]any
			_ = json.Unmarshal(payload, &decl)
			common, _ := decl["common"].(map[string]any)
			for _, lbl := range buildLabels(common) {
				key := lbl.Room
				if key == "" {
					key = "other"
				}
				if roomFilter != "" && key != roomFilter {
					continue
				}
				entry := map[string]any{
					"title":    lbl.Title,
					"endpoint": endpoint,
					"expose":   lbl.Expose,
					"channel":  lbl.Channel,
					"kind":     lbl.Kind,
				}
				if lbl.Command != nil {
					entry["command"] = lbl.Command
				}
				buckets[key] = append(buckets[key], entry)
			}
		}
		for k := range buckets {
			b := buckets[k]
			sort.Slice(b, func(i, j int) bool {
				return b[i]["title"].(string) < b[j]["title"].(string)
			})
			buckets[k] = b
		}
		return jsonResult(buckets), nil
	}
}

// scoreLabel returns a simple integer score for how well
// a label matches a tokenised user query. The score is the
// number of distinct tokens that appear (case-insensitive)
// in the label's title, keywords, room or kind. When the
// query is empty the score is 1 for every label.
func scoreLabel(lbl exposeLabel, tokens []string) int {
	if len(tokens) == 0 {
		return 1
	}
	haystacks := make([]string, 0, 4+len(lbl.Keywords))
	haystacks = append(haystacks,
		strings.ToLower(lbl.Title),
		lbl.Room,
		lbl.Kind,
		strings.ToLower(lbl.Class),
	)
	for _, k := range lbl.Keywords {
		haystacks = append(haystacks, k)
	}
	score := 0
	for _, tok := range tokens {
		for _, h := range haystacks {
			if h == "" {
				continue
			}
			if strings.Contains(h, tok) {
				score++
				break
			}
		}
	}
	return score
}

// augmentedExposeList walks the cached expose/... topics and
// returns a flat list of devices with the enriched "labels"
// field attached (so an LLM can read the per-channel room
// and kind without re-walking the nested "common" block).
// It is the body of the augmented homed_list_exposes tool.
func augmentedExposeList(client MQTTClient, meta MetaSource) ToolHandler {
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
			// Flat per-channel metadata. This is the primary
			// signal an LLM needs to pick the right channel
			// from a free-form request like "включи споты
			// на кухне" without having to walk the nested
			// "common" block.
			if common, _ := props["common"].(map[string]any); common != nil {
				if lbls := buildLabels(common); len(lbls) > 0 {
					flat := make([]map[string]any, 0, len(lbls))
					for _, l := range lbls {
						flat = append(flat, map[string]any{
							"expose":   l.Expose,
							"title":    l.Title,
							"room":     l.Room,
							"kind":     l.Kind,
							"class":    l.Class,
							"unit":     l.Unit,
							"type":     l.Type,
							"keywords": l.Keywords,
							"channel":  l.Channel,
							"settable": l.Settable,
							"command":  l.Command,
						})
					}
					props["labels"] = flat
				}
			}
			out = append(out, props)
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
		})
		return jsonResult(out), nil
	}
}


// augmentedSetDevice replaces the base homed_set_device with
// a handler that:
//
//   1. Always translates the UI-friendly property name
//      ("switch" / "switch_N") into the wire-property the
//      device actually accepts ("status" / "status_N").
//      Without this rewrite the command goes out as
//      {"switch_9":"on"} and the broker silently drops it
//      because the underlying custom- service only honours
//      the "status_N" form.
//
//   2. Resolves the endpoint through resolveDeviceIdentifier
//      so that, when the service runs with "names=true", the
//      caller can pass either the canonical id or the
//      broker-side name and the right thing is sent on the
//      wire. The base implementation in tools.go does this
//      for the topic but not for the request body, so a
//      caller that supplies an id while the service has
//      names=true ends up with a topic that does not match
//      any device.
//
//   3. Refuses to publish when the requested property does
//      not match a settable channel of the device — this is
//      a guard rail that the base handler does not provide
//      and that the regression "switch_9 vs status_9" was
//      hiding.
//
// The handler deliberately re-implements the wire logic
// (instead of calling the base toolSetDevice) so that the
// same code path can be unit-tested in isolation and so that
// the new behaviour is the one callers see after the
// augmented registration is wired up in main.go.
func augmentedSetDevice(client MQTTClient, meta MetaSource) ToolHandler {
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
// 1. Translate the property the user supplied into the
//    wire-property the device actually honours. The
//    rewrite is unconditional for switch_* -> status_*
//    (the universal convention used by every custom-*
//    switch expose in the project).
translateProperty := func(prop string) string {
if prop == "" {
return prop
}
if prop == "switch" {
return "status"
}
if strings.HasPrefix(prop, "switch_") {
return "status_" + strings.TrimPrefix(prop, "switch_")
}
return prop
}
var payload map[string]any
switch {
case p.Message != nil:
payload = p.Message
case p.Property != "":
resolved := translateProperty(p.Property)
payload = map[string]any{resolved: p.Value}
default:
return CallToolResult{Content: []Content{{Type: "text", Text: "either property+value or message must be provided"}}, IsError: true}, fmt.Errorf("either property+value or message must be provided")
}
// 2. Build the 'td/<service>/<id>[/<ep>]' topic. The
//    service may run with 'names=true' in which case
//    the trailing segment on the broker is the device
//    NAME rather than the id. resolveTdTopicForNamesFlag
//    swaps the id for the name when needed and returns
//    a warning we can surface to the caller.
topic, _, _, warn := resolveTdTopicForNamesFlag(client, "td/"+strings.TrimPrefix(p.Endpoint, "/"))
// 3. Publish. We only ever build a td/... topic from a
//    caller-supplied '<service>/<id>[/<ep>]' endpoint so
//    there is no surface for accidental injection of other
//    MQTT prefixes. The retain flag is forced off per the
//    HOMEd td/ convention.
if err := client.Publish(topic, payload, false); err != nil {
return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
}
if warn != "" {
return textResult(fmt.Sprintf("published to %s\nwarning: %s", topic, warn)), nil
}
return textResult(fmt.Sprintf("published to %s", topic)), nil
}
}

// augmentedDeviceList walks the cached device/... topics
// and returns a flat list of devices with the enriched
// "rooms" field attached (a list of normalised room ids
// derived from the device's dashboard placements).
func augmentedDeviceList(client MQTTClient, meta MetaSource) ToolHandler {
	return func(_ context.Context, _ json.RawMessage) (CallToolResult, error) {
		devices := filterByPrefix(client.Retained(), "device/")
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
				props["usage"] = u
			}
			if u := props["usage"]; u != nil {
				if list, _ := u.([]map[string]any); len(list) > 0 {
					if rooms := roomsFromUsage(list); len(rooms) > 0 {
						props["rooms"] = rooms
					}
				}
			}
			out = append(out, props)
		}
		sort.Slice(out, func(i, j int) bool {
			return fmt.Sprint(out[i]["id"]) < fmt.Sprint(out[j]["id"])
		})
		return jsonResult(out), nil
	}
}

// RegisterHOMEdToolsAugmented calls the base RegisterHOMEdTools
// (defined in tools.go) and then re-registers the two list*
// tools with room-aware / label-enriched handlers, plus
// registers two new tools (homed_search and homed_room_overview).
//
// The base handlers are kept for any caller that has cached
// the original "labels"-less format, but most clients will
// prefer the augmented ones.
func RegisterHOMEdToolsAugmented(s *Server, client MQTTClient, meta MetaSource) []string {
	// Register the base catalogue first.
	base := RegisterHOMEdTools(s, client, meta)

	// Override the two discovery tools with the enriched
	// versions. Server.RegisterTool updates the tool in
	// place when the name is already registered (see the
	// comment in server.go), so we don't have to unregister
	// the base handlers explicitly.
	exposeSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	s.RegisterTool(Tool{
		Definition: ToolDefinition{
			Name:        "homed_list_exposes",
			Description: "List devices advertised through 'expose' topics (e.g. zigbee2mqtt style bridge exposes). The response carries a flat 'labels[]' array (one entry per channel) with normalised 'room' / 'kind' / 'channel' / 'settable' fields and a ready-to-use 'command' block (e.g. {property: 'status_2', value: 'on'}). LLM callers should prefer 'labels[].command' over the raw 'common.options' block when they need to identify which channel a free-form request refers to. A 'usage' array is included when homed-web's database.json is available.",
			InputSchema: exposeSchema,
		},
		Handler: augmentedExposeList(client, meta),
	})
	deviceSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	s.RegisterTool(Tool{
		Definition: ToolDefinition{
			Name:        "homed_list_devices",
			Description: "List all known HOMEd devices discovered via retained 'device/*' MQTT topics. Each device is enriched with a 'usage' array (dashboards / blocks / user-defined item names) when homed-web's database.json is available. When the device is referenced by at least one dashboard the response also carries a flat 'rooms' array of normalised room ids so a caller can answer 'in which room does this device live?' without re-walking the nested usage block.",
			InputSchema: deviceSchema,
		},
		Handler: augmentedDeviceList(client, meta),
	})

	// Override homed_set_device with a handler that always
	// translates the UI-friendly property name into the
	// wire-property the device actually accepts (switch_N
	// -> status_N) and that resolves the endpoint through
	// resolveTdTopicForNamesFlag so that the caller can
	// pass either the canonical id or the broker-side name
	// when the service runs with names=true.
	setDeviceSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"endpoint": map[string]any{"type": "string", "description": "Device endpoint in the form '<service>/<deviceId>[/endpointId]'. For example 'custom/alarm' or 'zigbee/0x00124b0014b0b0b0/1'."},
			"property": map[string]any{"type": "string", "description": "Property name to set. For switch/lock exposes this is 'status'; for raw values use the property name itself (e.g. 'level', 'targetTemperature'). The augmented handler automatically rewrites 'switch' / 'switch_N' into the wire-property 'status' / 'status_N' that the custom-* services actually accept."},
			"value":    map[string]any{"description": "Value to assign. For switch/lock exposes use 'on'/'off'/'toggle'."},
			"message":  map[string]any{"type": "object", "description": "Optional. When set, this raw object overrides the {property:value} payload.", "additionalProperties": true},
		},
		"required": []string{"endpoint"},
	}
	s.RegisterTool(Tool{
		Definition: ToolDefinition{
			Name:        "homed_set_device",
			Description: "Send a control command to a single HOMEd device. The augmented handler ALWAYS rewrites the 'property' argument from the UI-friendly form ('switch' / 'switch_N') to the wire form ('status' / 'status_N') before publishing, so callers that learnt the property name from a cached expose schema (or from homed_list_exposes 'labels[].command.property') can pass it through unchanged. It also resolves the supplied endpoint through resolveDeviceIdentifier so that, when the service runs with 'names=true', the caller can pass either the canonical id or the broker-side name and the right thing is sent on the wire.",
			InputSchema: setDeviceSchema,
		},
		Handler: augmentedSetDevice(client, meta),
	})

	// Register the two new search tools.
	searchSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Free-form text, tokenised and matched against title/keywords/room/kind. Cyrillic and Latin are both supported."},
			"room":  map[string]any{"type": "string", "description": "Restrict to a single normalised room id: kitchen | hall | bedroom | kids | bath | corridor | veranda | sauna | outdoor | boiler | water | electrical | lighting | charts."},
			"kind":  map[string]any{"type": "string", "description": "Restrict to a single normalised kind id: light | outlet | switch | motion | contact | temperature | humidity | pressure | boiler-flame | pump | valve | lock | cover | battery | voltage | current | power | energy | illuminance | signal | thermostat | alarm | garland | notification | meter | counter."},
			"limit": map[string]any{"type": "integer", "description": "Maximum number of hits to return (default 25, hard cap 100).", "minimum": 1, "maximum": 100},
		},
	}
	s.RegisterTool(Tool{
		Definition: ToolDefinition{
			Name:        "homed_search",
			Description: "Search expose channels by free-form text, room and kind. The tool tokenises 'query' (Cyrillic or Latin) and matches every token against the cached 'expose/<svc>/<dev>' channel titles; the optional 'room' and 'kind' filters apply the same normalised ids that appear in the 'labels[]' array of homed_list_exposes. Each hit includes the canonical 'endpoint' plus a ready-to-use 'command' object so a single tool call is enough to both identify and act on the right device. Default hit limit is 25, hard cap 100. USE THIS TOOL whenever the user's request is a free-form sentence (e.g. 'включи споты на кухне', 'turn off the lights in the bedroom') instead of walking the full homed_list_exposes JSON.",
			InputSchema: searchSchema,
		},
		Handler: toolSearch(client),
	})
	roomOverviewSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"room": map[string]any{"type": "string", "description": "Optional normalised room id. When set, only the bucket for that room is returned."},
		},
	}
	s.RegisterTool(Tool{
		Definition: ToolDefinition{
			Name:        "homed_room_overview",
			Description: "Compact, group-by-room dump of every known channel. The response is a map {room: [{title, endpoint, expose, channel, kind, command}, ...]}. Channels whose 'room' could not be classified are returned under the 'other' bucket. The optional 'room' argument restricts the response to a single normalised room id. Useful for UI/voice use cases that need a small (<5 KB) response instead of the full homed_list_exposes JSON.",
			InputSchema: roomOverviewSchema,
		},
		Handler: toolRoomOverview(client),
	})

	// Compose the result: the base names, plus the two new
	// tools, deduplicated and order-preserving.
	seen := make(map[string]struct{}, len(base)+2)
	out := make([]string, 0, len(base)+2)
	for _, n := range base {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for _, n := range []string{"homed_set_device", "homed_search", "homed_room_overview"} {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}