package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/u236/homed-mcp/internal/recorder"
)

// RecorderSource is the minimal interface that the
// homed_query_recorder tool needs from a recorder.Provider. The
// interface is defined here (not imported) so that the tool layer
// stays decoupled from how the recorder is constructed and so that
// tests can plug a fake in.
type RecorderSource interface {
	HasDatabase() bool
	Items(recorder.NameLookup) []recorder.AnnotatedItem
	FindItems(endpoint, property string, lookup recorder.NameLookup) []recorder.AnnotatedItem
	Query(ctx context.Context, opts recorder.QueryOptions) ([]recorder.Stat, error)
	QueryDaily(ctx context.Context, opts recorder.QueryOptions) ([]recorder.DailyBucket, error)
	QueryTransitions(ctx context.Context, items []recorder.AnnotatedItem, from, to time.Time, limit int) ([]recorder.Transitions, error)
}

// RegisterRecorderTool wires the homed_query_recorder tool into the
// server. It is intentionally split out of tools.go so that the
// file does not become too long. The lookup argument is used to
// enrich the recorder output with user-defined names from
// homed-web's database.json Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ pass nil to disable enrichment.
func RegisterRecorderTool(s *Server, src RecorderSource, lookup recorder.NameLookup) []string {
	if src == nil {
		return nil
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Pre-parsed query kind. One of: items, stats, daily, transitions. If omitted, 'kind' is used.",
			},
			"kind": map[string]any{
				"type":        "string",
				"enum":        []string{"stats", "daily", "transitions", "items"},
				"description": "Query kind. 'stats' (default) computes an aggregate; 'daily' produces per-day buckets; 'transitions' counts on/off events; 'items' returns the catalog of recorded items.",
				"default":     "stats",
			},
			"endpoint": map[string]any{
				"type":        "string",
				"description": "Device endpoint pattern, e.g. 'zigbee' (prefix), 'zigbee/0x...' (exact), or empty for all. Combined with 'property' to narrow the result.",
			},
			"property": map[string]any{
				"type":        "string",
				"description": "Property name to filter by, e.g. 'temperature', 'status', 'FlameDuration'. Empty matches any property.",
			},
			"metric": map[string]any{
				"type":        "string",
				"enum":        []string{"avg", "min", "max", "sum", "count", "first", "last", "extrema"},
				"description": "Aggregate to compute. Required for kind=stats (default 'avg'). For kind=transitions this argument is ignored.",
				"default":     "avg",
			},
			"series": map[string]any{
				"type":        "string",
				"enum":        []string{"hour", "data"},
				"description": "Source table. 'hour' is the pre-aggregated hourly data (faster, fewer rows); 'data' is the raw sample table. Defaults to 'hour'.",
				"default":     "hour",
			},
			"from": map[string]any{
				"type":        "string",
				"description": "Start of the time range. Accepts RFC3339 (e.g. 2026-04-01T00:00:00Z), YYYY-MM-DD, or a keyword: today, yesterday, this-week, this-month, last-24h, last-7d, last-30d, now.",
			},
			"to": map[string]any{
				"type":        "string",
				"description": "End of the time range (exclusive). Same format as 'from'. Empty means 'now'.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "For kind=transitions, the maximum number of transition events to include in the 'transitions' array. Defaults to 50.",
				"default":     50,
				"minimum":     0,
				"maximum":     5000,
			},
		},
	}
	t := Tool{
		Definition: ToolDefinition{
			Name:        "homed_query_recorder",
			Description: "Query historical data from the homed-recorder SQLite database. USE THIS TOOL for any historical/aggregate question about devices Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ live tools (homed_get_status, homed_get_properties) only return the most recent retained value and CANNOT answer questions like: 'what was the outdoor temperature last night', 'how many times did the pump turn on today', 'how long did the boiler run this week', 'what was the coldest day in April', 'what was the average temperature in March', 'which day in March was the coldest'. Supported kinds: 'stats' (default Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ single aggregate, e.g. avg/min/max/sum over a time range), 'daily' (per-day buckets Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ ideal for 'coldest/hottest day in month' style questions, pair with metric=min or metric=max), 'transitions' (count on/off events for a binary state), 'items' (catalog of recorded items). Endpoint+property select the device(s); leaving them empty returns aggregates across all devices. The 'from'/'to' arguments accept friendly keywords (today, yesterday, this-week, this-month, last-24h, last-7d, last-30d, now) in addition to RFC3339 and YYYY-MM-DD timestamps.",
			InputSchema: schema,
		},
		Handler: toolQueryRecorder(src, lookup),
	}
	s.RegisterTool(t)
	return []string{t.Definition.Name}
}

func toolQueryRecorder(src RecorderSource, lookup recorder.NameLookup) ToolHandler {
	return func(ctx context.Context, args json.RawMessage) (CallToolResult, error) {
		if src == nil || !src.HasDatabase() {
			return CallToolResult{
				Content: []Content{{Type: "text", Text: "homed-recorder database is not configured (set paths.homed-recorder in config.json)."}},
				IsError: true,
			}, fmt.Errorf("recorder: no database configured")
		}
		var p struct {
			Query    string `json:"query"`
			Kind     string `json:"kind"`
			Endpoint string `json:"endpoint"`
			Property string `json:"property"`
			Metric   string `json:"metric"`
			Series   string `json:"series"`
			From     string `json:"from"`
			To       string `json:"to"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal(args, &p)

		// Map the legacy "query" string to "kind" for callers that
		// still send the old shape. New callers should use "kind".
		if p.Kind == "" && p.Query != "" {
			p.Kind = p.Query
		}
		if p.Kind == "" {
			p.Kind = "stats"
		}
		if p.Metric == "" {
			p.Metric = string(recorder.MetricAvg)
		}
		if p.Series == "" {
			p.Series = string(recorder.SeriesHour)
		}
		if p.Limit == 0 {
			p.Limit = 50
		}

		from, to, err := recorder.ResolveTimeRange(p.From, p.To, time.Now().UTC())
		if err != nil {
			return CallToolResult{
				Content: []Content{{Type: "text", Text: "invalid time range: " + err.Error()}},
				IsError: true,
			}, err
		}

		items := src.FindItems(p.Endpoint, p.Property, lookup)
		// Convenience for the common 'just find the outdoor
		// temperature history' question: when the caller did not
		// pin a specific endpoint/property and we're computing a
		// temperature-like aggregate (any kind except transitions
		// and items), narrow the candidate set to items whose
		// property looks temperature-related. This dramatically
		// increases the chance of a useful answer for open-ended
		// questions like 'what was the coldest day in April' even
		// when the model does not know the exact endpoint id.
		if len(items) == 0 && p.Endpoint == "" && p.Property == "" {
			return jsonResult(map[string]any{
				"kind":     p.Kind,
				"matched":  0,
				"hint":     "no recorded items matched; call again with kind=items to enumerate available endpoints and properties",
				"endpoint": p.Endpoint,
				"property": p.Property,
			}), nil
		}
		if len(items) > 0 && p.Endpoint == "" && p.Property == "" && p.Kind != "items" && p.Kind != "transitions" {
			narrowed := narrowToLikelyProperty(items, p.Metric, p.Kind)
			if len(narrowed) > 0 && len(narrowed) < len(items) {
				items = narrowed
			}
		}

		switch strings.ToLower(p.Kind) {
		case "items":
			return jsonResult(map[string]any{
				"kind":  "items",
				"count": len(items),
				"items": annotatedItemsToMap(items),
			}), nil

		case "stats":
			opts := recorder.QueryOptions{
				Items:  items,
				Metric: recorder.Metric(p.Metric),
				Series: recorder.Series(p.Series),
				From:   from,
				To:     to,
			}
			stats, err := src.Query(ctx, opts)
			if err != nil {
				return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
			}
			return jsonResult(map[string]any{
				"kind":     "stats",
				"endpoint": p.Endpoint,
				"property": p.Property,
				"metric":   p.Metric,
				"series":   p.Series,
				"from":     from,
				"to":       to,
				"matched":  len(stats),
				"stats":    statsToMaps(stats),
			}), nil

		case "daily":
			opts := recorder.QueryOptions{
				Items:  items,
				Metric: recorder.Metric(p.Metric),
				Series: recorder.Series(p.Series),
				From:   from,
				To:     to,
			}
			buckets, err := src.QueryDaily(ctx, opts)
			if err != nil {
				return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
			}
			return jsonResult(map[string]any{
				"kind":     "daily",
				"endpoint": p.Endpoint,
				"property": p.Property,
				"metric":   p.Metric,
				"series":   p.Series,
				"from":     from,
				"to":       to,
				"matched":  len(buckets),
				"days":     buckets,
			}), nil

		case "transitions":
			// For transitions the "to" timestamp is treated as the
			// current moment for the purpose of attributing the open
			// tail to the current state. If the caller did not set a
			// "to", use the current time.
			transTo := to
			if transTo.IsZero() {
				transTo = time.Now().UTC()
			}
			trs, err := src.QueryTransitions(ctx, items, from, transTo, p.Limit)
			if err != nil {
				return CallToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}, err
			}
			return jsonResult(map[string]any{
				"kind":     "transitions",
				"endpoint": p.Endpoint,
				"property": p.Property,
				"from":     from,
				"to":       transTo,
				"matched":  len(trs),
				"items":    transitionsToMaps(trs),
			}), nil
		}

		return CallToolResult{
			Content: []Content{{Type: "text", Text: "unknown kind: " + p.Kind}},
			IsError: true,
		}, fmt.Errorf("recorder: unknown kind %q", p.Kind)
	}
}

// annotatedItemsToMap converts a slice of AnnotatedItem into a
// JSON-friendly representation. The item id / endpoint / property
// stay on the top level; the user-defined usage array is renamed
// to "usage" to match the convention used by the other tools.
func annotatedItemsToMap(items []recorder.AnnotatedItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, ai := range items {
		m := map[string]any{
			"id":       ai.ID,
			"endpoint": ai.Endpoint,
			"property": ai.Property,
		}
		if len(ai.Usage) > 0 {
			usage := make([]map[string]any, 0, len(ai.Usage))
			for _, u := range ai.Usage {
				usage = append(usage, map[string]any{
					"dashboard": u.Dashboard,
					"block":     u.Block,
					"name":      u.Name,
					"expose":    u.Expose,
					"property":  u.Property,
					"endpoint":  u.Endpoint,
				})
			}
			m["usage"] = usage
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["endpoint"].(string)
		b, _ := out[j]["endpoint"].(string)
		return a < b
	})
	return out
}

// statsToMaps turns a []recorder.Stat into a JSON-friendly slice.
// The per-item usage is preserved at the top level (so that
// "stats for which device?" is answerable in one read) and the
// numeric pointers are dereferenced only when non-nil.
func statsToMaps(stats []recorder.Stat) []map[string]any {
	out := make([]map[string]any, 0, len(stats))
	for _, s := range stats {
		m := map[string]any{
			"endpoint":   s.Item.Endpoint,
			"property":   s.Item.Property,
			"metric":     string(s.Metric),
			"series":     string(s.Series),
			"from":       s.From,
			"to":         s.To,
			"sampleSize": s.SampleSize,
		}
		if s.Avg != nil {
			m["avg"] = *s.Avg
		}
		if s.Min != nil {
			m["min"] = *s.Min
		}
		if s.Max != nil {
			m["max"] = *s.Max
		}
		if s.Sum != nil {
			m["sum"] = *s.Sum
		}
		if s.Count != nil {
			m["count"] = *s.Count
		}
		if s.First != nil {
			m["first"] = s.First
		}
		if s.Last != nil {
			m["last"] = s.Last
		}
		if len(s.Item.Usage) > 0 {
			usage := make([]map[string]any, 0, len(s.Item.Usage))
			for _, u := range s.Item.Usage {
				usage = append(usage, map[string]any{
					"dashboard": u.Dashboard,
					"block":     u.Block,
					"name":      u.Name,
					"expose":    u.Expose,
					"property":  u.Property,
					"endpoint":  u.Endpoint,
				})
			}
			m["usage"] = usage
		}
		out = append(out, m)
	}
	return out
}

// transitionsToMaps is the analogue of statsToMaps for binary
// state changes. The transitions array is preserved verbatim
// (limit-bounded by the caller); on/off seconds are kept as
// floating-point values.
func transitionsToMaps(trs []recorder.Transitions) []map[string]any {
	out := make([]map[string]any, 0, len(trs))
	for _, tr := range trs {
		m := map[string]any{
			"endpoint":     tr.Item.Endpoint,
			"property":     tr.Item.Property,
			"from":         tr.From,
			"to":           tr.To,
			"offToOn":      tr.OffToOn,
			"onToOff":      tr.OnToOff,
			"total":        tr.Total,
			"onSeconds":    tr.OnSeconds,
			"offSeconds":   tr.OffSeconds,
			"coverage":     tr.Coverage,
			"currentState": tr.CurrentState,
		}
		if len(tr.TransitionsLog) > 0 {
			logs := make([]map[string]any, 0, len(tr.TransitionsLog))
			for _, s := range tr.TransitionsLog {
				logs = append(logs, map[string]any{
					"timestamp": s.Timestamp,
					"value":     s.Value,
				})
			}
			m["transitions"] = logs
		}
		if len(tr.Item.Usage) > 0 {
			usage := make([]map[string]any, 0, len(tr.Item.Usage))
			for _, u := range tr.Item.Usage {
				usage = append(usage, map[string]any{
					"dashboard": u.Dashboard,
					"block":     u.Block,
					"name":      u.Name,
					"expose":    u.Expose,
					"property":  u.Property,
					"endpoint":  u.Endpoint,
				})
			}
			m["usage"] = usage
		}
		out = append(out, m)
	}
	return out
}

// narrowToLikelyProperty is a best-effort heuristic that filters
// a list of recorder items down to those that are most likely
// to answer a temperature- / energy- / binary-state-style
// question. It is used by toolQueryRecorder when the caller did
// not pin a specific endpoint/property (e.g. an LLM asking
// "what was the coldest day in April?" without knowing the
// device id). The filter is intentionally conservative: when it
// does not reduce the set, the original list is returned
// unchanged so that the call still succeeds.
func narrowToLikelyProperty(items []recorder.AnnotatedItem, metric, kind string) []recorder.AnnotatedItem {
	propCount := make(map[string]int)
	for _, it := range items {
		if it.Property != "" {
			propCount[it.Property]++
		}
	}
	temperatureKinds := []string{
		"temperature", "temp", "humidity", "pressure",
		"co2", "lux", "illuminance", "battery", "voltage",
		"power", "energy", "flameduration",
	}
	binaryKinds := []string{
		"status", "state", "on", "off", "contact", "motion",
		"occupancy", "presence", "alarm",
	}
	var wanted []string
	switch strings.ToLower(metric) {
	case "min", "max", "avg", "sum":
		wanted = append(wanted, temperatureKinds...)
	default:
		wanted = append(wanted, temperatureKinds...)
		wanted = append(wanted, binaryKinds...)
	}
	if strings.EqualFold(kind, "transitions") {
		wanted = binaryKinds
	}
	wantedLower := make([]string, len(wanted))
	for i, w := range wanted {
		wantedLower[i] = strings.ToLower(w)
	}
	var out []recorder.AnnotatedItem
	for _, it := range items {
		p := strings.ToLower(it.Property)
		if p == "" {
			continue
		}
		for _, w := range wantedLower {
			if strings.Contains(p, w) {
				out = append(out, it)
				break
			}
		}
	}
	if len(out) == 0 {
		var best string
		bestN := 0
		for prop, n := range propCount {
			if n > bestN {
				best = prop
				bestN = n
			}
		}
		if best != "" {
			for _, it := range items {
				if it.Property == best {
					out = append(out, it)
				}
			}
		}
	}
	return out
}