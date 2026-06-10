package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// buildCompactIndex scans the retained cache plus the homed-web
// meta and produces a flat list of "label -> (endpoint, property,
// status)" lines, suitable for pasting into the homed overview
// tool response so the language model has every friendly name in
// context without having to issue a separate discovery call.
//
// The function is intentionally a free helper (no MCP types in
// the signature) so the unit tests can call it with a fake
// retained cache and a fake meta source.
//
// The output is sorted by (endpoint, label) for determinism so
// the model can rely on a stable order across calls. Each line
// is shaped "<user-name>  ->  <endpoint> [, property=<property>]
// [, status=<status>]". About 6-15 KB for a typical 50-device
// home.
func buildCompactIndex(cache map[string][]byte, meta MetaSource) []string {
	if len(cache) == 0 {
		return nil
	}
	type row struct {
		label    string
		endpoint string
		property string
		status   string
	}
	rows := make([]row, 0, 32)
	seen := make(map[string]bool) // dedup by label+endpoint+property
	for _, t := range sortedKeys(cache) {
		if !strings.HasPrefix(t, "status/") {
			continue
		}
		endpoint := strings.TrimPrefix(t, "status/")
		payload := cache[t]
		var status map[string]any
		_ = json.Unmarshal(payload, &status)
		for _, m := range meta.LookupEndpoint(endpoint) {
			item := m.Item
			if item.Name == "" {
				continue
			}
			property := item.Property
			channel := item.Expose
			if property == "" && channel == "" {
				continue
			}
			key := endpoint + "|" + property + "|" + channel
			if seen[key] {
				continue
			}
			seen[key] = true
			statusText := ""
			if status != nil {
				if property != "" {
					if v, ok := status[property]; ok {
						statusText = fmt.Sprintf("%v", v)
					}
				}
				if statusText == "" && channel != "" {
					if v, ok := status[channel]; ok {
						statusText = fmt.Sprintf("%v", v)
					}
				}
			}
			rows = append(rows, row{
				label:    item.Name,
				endpoint: endpoint,
				property: property,
				status:   statusText,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].endpoint != rows[j].endpoint {
			return rows[i].endpoint < rows[j].endpoint
		}
		return rows[i].label < rows[j].label
	})
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		b.WriteString(r.label)
		b.WriteString("  ->  ")
		b.WriteString(r.endpoint)
		if r.property != "" {
			b.WriteString("  property=")
			b.WriteString(r.property)
		}
		if r.status != "" {
			b.WriteString("  status=")
			b.WriteString(r.status)
		}
		out = append(out, b.String())
	}
	return out
}
