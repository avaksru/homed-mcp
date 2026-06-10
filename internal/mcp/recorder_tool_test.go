package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/u236/homed-mcp/internal/recorder"
)

// fakeRecorder is a hand-rolled RecorderSource used by the unit
// tests for homed_query_recorder. It returns a small fixed dataset
// and records the last call so we can assert on it.
type fakeRecorder struct {
	db          bool
	items       []recorder.AnnotatedItem
	lastItems   []recorder.AnnotatedItem
	lastOpts    recorder.QueryOptions
	lastFrom    time.Time
	lastTo      time.Time
	lastLimit   int
	statsResult []recorder.Stat
	statsErr    error
	dailyResult []recorder.DailyBucket
	dailyErr    error
	transResult []recorder.Transitions
	transErr    error
}

func (f *fakeRecorder) HasDatabase() bool { return f.db }
func (f *fakeRecorder) Items(lookup recorder.NameLookup) []recorder.AnnotatedItem {
	return f.items
}
func (f *fakeRecorder) FindItems(endpoint, property string, lookup recorder.NameLookup) []recorder.AnnotatedItem {
	f.lastItems = nil
	for _, it := range f.items {
		if endpoint != "" && !strings.HasPrefix(it.Endpoint, endpoint+"/") && it.Endpoint != endpoint {
			continue
		}
		if property != "" && it.Property != property {
			continue
		}
		f.lastItems = append(f.lastItems, it)
	}
	return f.lastItems
}
func (f *fakeRecorder) Query(ctx context.Context, opts recorder.QueryOptions) ([]recorder.Stat, error) {
	f.lastOpts = opts
	return f.statsResult, f.statsErr
}
func (f *fakeRecorder) QueryDaily(ctx context.Context, opts recorder.QueryOptions) ([]recorder.DailyBucket, error) {
	f.lastOpts = opts
	return f.dailyResult, f.dailyErr
}
func (f *fakeRecorder) QueryTransitions(ctx context.Context, items []recorder.AnnotatedItem, from, to time.Time, limit int) ([]recorder.Transitions, error) {
	f.lastItems = items
	f.lastFrom = from
	f.lastTo = to
	f.lastLimit = limit
	return f.transResult, f.transErr
}

func TestRecorderTool_NoDatabase(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: false}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind": "stats", "endpoint": "zigbee", "property": "temperature",
	})
	if !out.IsError {
		t.Fatal("expected IsError=true when database not configured")
	}
	if !strings.Contains(textOf(out), "not configured") {
		t.Errorf("unexpected error text: %s", textOf(out))
	}
}

func TestRecorderTool_NoMatch(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind": "stats", "endpoint": "zigbee/zz", "property": "temperature",
	})
	var resp map[string]any
	if err := json.Unmarshal([]byte(textOf(out)), &resp); err != nil {
		t.Fatalf("response is not JSON: %v / %s", err, textOf(out))
	}
	if matched, _ := resp["matched"].(float64); matched != 0 {
		t.Errorf("matched = %v, want 0", resp["matched"])
	}
}

func TestRecorderTool_Stats(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true, items: []recorder.AnnotatedItem{
		{Item: recorder.Item{ID: 1, Endpoint: "zigbee/aa", Property: "temperature"}},
		{Item: recorder.Item{ID: 2, Endpoint: "zigbee/bb", Property: "temperature"}},
	}, statsResult: []recorder.Stat{
		{Item: recorder.AnnotatedItem{Item: recorder.Item{ID: 1, Endpoint: "zigbee/aa", Property: "temperature"}}, SampleSize: 24, Avg: floatPtr(21.5)},
		{Item: recorder.AnnotatedItem{Item: recorder.Item{ID: 2, Endpoint: "zigbee/bb", Property: "temperature"}}, SampleSize: 24, Avg: floatPtr(10.5)},
	}}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind":     "stats",
		"endpoint": "zigbee",
		"property": "temperature",
		"metric":   "avg",
		"series":   "hour",
		"from":     "yesterday",
		"to":       "today",
	})
	if out.IsError {
		t.Fatalf("expected success, got: %s", textOf(out))
	}
	if src.lastOpts.Metric != recorder.MetricAvg {
		t.Errorf("metric = %q, want avg", src.lastOpts.Metric)
	}
	if src.lastOpts.Series != recorder.SeriesHour {
		t.Errorf("series = %q, want hour", src.lastOpts.Series)
	}
	if src.lastOpts.From.IsZero() {
		t.Error("from should be non-zero (yesterday resolved)")
	}
	if src.lastOpts.To.IsZero() {
		t.Error("to should be non-zero (today resolved)")
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(textOf(out)), &resp); err != nil {
		t.Fatalf("response is not JSON: %v / %s", err, textOf(out))
	}
	if matched, _ := resp["matched"].(float64); matched != 2 {
		t.Errorf("matched = %v, want 2", resp["matched"])
	}
	stats, _ := resp["stats"].([]any)
	if len(stats) != 2 {
		t.Fatalf("stats = %d, want 2", len(stats))
	}
	first := stats[0].(map[string]any)
	if first["endpoint"] != "zigbee/aa" {
		t.Errorf("first.endpoint = %v", first["endpoint"])
	}
}

func TestRecorderTool_Daily(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true, items: []recorder.AnnotatedItem{
		{Item: recorder.Item{ID: 1, Endpoint: "zigbee/aa", Property: "temperature"}},
	}, dailyResult: []recorder.DailyBucket{
		{Day: "2026-04-15", Count: 24, Min: 5.5, Max: 12.0, Avg: 8.7},
		{Day: "2026-04-16", Count: 24, Min: 3.0, Max: 9.0, Avg: 6.5},
	}}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind":     "daily",
		"endpoint": "zigbee/aa",
		"property": "temperature",
		"metric":   "min",
		"from":     "2026-04-01",
		"to":       "2026-05-01",
	})
	if out.IsError {
		t.Fatalf("expected success, got: %s", textOf(out))
	}
	if src.lastOpts.Metric != recorder.MetricMin {
		t.Errorf("metric = %q, want min", src.lastOpts.Metric)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(textOf(out)), &resp)
	days, _ := resp["days"].([]any)
	if len(days) != 2 {
		t.Fatalf("days = %d, want 2", len(days))
	}
}

func TestRecorderTool_Transitions(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true, items: []recorder.AnnotatedItem{
		{Item: recorder.Item{ID: 1, Endpoint: "custom/pump", Property: "status"}},
	}, transResult: []recorder.Transitions{
		{Item: recorder.AnnotatedItem{Item: recorder.Item{ID: 1, Endpoint: "custom/pump", Property: "status"}}, OffToOn: 3, OnToOff: 3, Total: 6, CurrentState: "off"},
	}}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind":     "transitions",
		"endpoint": "custom/pump",
		"property": "status",
		"from":     "today",
		"limit":    20,
	})
	if out.IsError {
		t.Fatalf("expected success, got: %s", textOf(out))
	}
	if src.lastLimit != 20 {
		t.Errorf("limit = %d, want 20", src.lastLimit)
	}
	if src.lastFrom.IsZero() {
		t.Error("from should be resolved (today)")
	}
	if src.lastTo.IsZero() {
		t.Error("to should default to now")
	}
}

func TestRecorderTool_Items(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true, items: []recorder.AnnotatedItem{
		{Item: recorder.Item{ID: 1, Endpoint: "zigbee/aa", Property: "temperature"}},
		{Item: recorder.Item{ID: 2, Endpoint: "custom/boiler", Property: "FlameDuration"}},
	}}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{"kind": "items"})
	if out.IsError {
		t.Fatalf("expected success, got: %s", textOf(out))
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(textOf(out)), &resp)
	items, _ := resp["items"].([]any)
	if len(items) != 2 {
		t.Errorf("items = %d, want 2", len(items))
	}
}

func TestRecorderTool_BadTime(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind": "stats",
		"from": "bogus",
	})
	if !out.IsError {
		t.Fatal("expected IsError=true for bad time")
	}
}

func TestRecorderTool_BadKind(t *testing.T) {
	srv := NewServer("test", "0.0.0")
	src := &fakeRecorder{db: true, items: []recorder.AnnotatedItem{
		{Item: recorder.Item{ID: 1, Endpoint: "zigbee/aa", Property: "temperature"}},
	}}
	RegisterRecorderTool(srv, src, nil)
	out := callToolForTest(t, srv, "homed_query_recorder", map[string]any{
		"kind":     "wat",
		"endpoint": "zigbee/aa",
	})
	if !out.IsError {
		t.Fatal("expected IsError=true for unknown kind")
	}
}

// callToolForTest sends a tools/call frame to the server and
// returns the resulting CallToolResult. Mirrors runServer from
// server_test.go but stays local to the recorder tests.
func callToolForTest(t *testing.T, srv *Server, name string, args map[string]any) CallToolResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	tool, ok := srv.tools[name]
	if !ok {
		t.Fatalf("tool %q not registered", name)
	}
	var params CallToolParams
	if err := json.Unmarshal([]byte(`{"name":"`+name+`","arguments":`+string(raw)+`}`), &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	out, err := tool.Handler(context.Background(), params.Arguments)
	if err != nil {
		t.Logf("handler returned err: %v", err)
	}
	return out
}

func textOf(r CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	return r.Content[0].Text
}

func floatPtr(v float64) *float64 { return &v }