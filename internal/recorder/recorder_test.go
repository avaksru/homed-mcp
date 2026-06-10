// Unit tests for the recorder package. They use an in-memory
// SQLite database (modernc.org/sqlite supports ":memory:") seeded
// with a few rows so the queries can be exercised end-to-end.
package recorder

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// dayStart is the UTC-aligned start of the seeded day (2023-11-15).
// Every test fixture row uses timestamps derived from this constant
// so the (timestamp/86400000)*86400000 bucketing used by QueryDaily
// lands in a single day bucket.
const dayStart int64 = 1700006400000

// seedDB creates a fresh in-memory database with the recorder's
// schema and a small but representative dataset. The test fixtures
// cover numeric items (hour table) and discrete items (data
// table). Returned is the open *sql.DB вЂ” the caller closes it.
func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?mode=rw&cache=shared")
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	ddl := `
		CREATE TABLE item  (id INTEGER PRIMARY KEY AUTOINCREMENT, endpoint TEXT NOT NULL, property TEXT NOT NULL, debounce INTEGER NOT NULL, threshold REAL NOT NULL);
		CREATE TABLE data  (id INTEGER PRIMARY KEY AUTOINCREMENT, item_id INTEGER REFERENCES item(id) ON DELETE CASCADE, timestamp INTEGER NOT NULL, value TEXT NOT NULL);
		CREATE TABLE hour  (id INTEGER PRIMARY KEY AUTOINCREMENT, item_id INTEGER REFERENCES item(id) ON DELETE CASCADE, timestamp INTEGER NOT NULL, avg REAL NOT NULL, min REAL NOT NULL, max REAL NOT NULL);
	`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("ddl: %v", err)
	}
	// Items.
	if _, err := db.Exec(`INSERT INTO item (id, endpoint, property, debounce, threshold) VALUES
		(1, 'zigbee/aa', 'temperature', 0, 0),
		(2, 'zigbee/bb', 'temperature', 0, 0),
		(3, 'custom/boiler', 'FlameDuration', 0, 0),
		(4, 'custom/pump', 'status_1', 0, 0)`); err != nil {
		t.Fatalf("insert items: %v", err)
	}
	// Hour data: 24 hours of two temperature sensors, starting at
	// dayStart so the daily rollup produces a single bucket.
	for h := 0; h < 24; h++ {
		ts := dayStart + int64(h)*3600*1000
		if _, err := db.Exec(`INSERT INTO hour (item_id, timestamp, avg, min, max) VALUES (?, ?, ?, ?, ?)`,
			1, ts, 20.0+float64(h)/10.0, 19.0+float64(h)/10.0, 21.0+float64(h)/10.0); err != nil {
			t.Fatalf("insert hour 1: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO hour (item_id, timestamp, avg, min, max) VALUES (?, ?, ?, ?, ?)`,
			2, ts, 10.0+float64(h)/20.0, 9.0+float64(h)/20.0, 11.0+float64(h)/20.0); err != nil {
			t.Fatalf("insert hour 2: %v", err)
		}
	}
	// Flame duration: 1 minute per hour for 5 hours.
	for h := 0; h < 5; h++ {
		ts := dayStart + int64(h)*3600*1000
		if _, err := db.Exec(`INSERT INTO hour (item_id, timestamp, avg, min, max) VALUES (?, ?, ?, ?, ?)`,
			3, ts, 60, 60, 60); err != nil {
			t.Fatalf("insert hour 3: %v", err)
		}
	}
	// Pump status: on/off transitions in the data table.
	// Timestamps are 10-minute samples starting at the day boundary.
	pumpSamples := []struct {
		ts    int64
		value string
	}{
		{dayStart - 600000, "off"}, // 10 min before
		{dayStart, "on"},
		{dayStart + 600000, "off"},
		{dayStart + 1200000, "on"},
		{dayStart + 1800000, "off"},
		{dayStart + 2400000, "on"},
		// [unavailable] should be ignored by transitions.
		{dayStart + 3000000, "[unavailable]"},
		{dayStart + 3600000, "off"},
	}
	for _, s := range pumpSamples {
		if _, err := db.Exec(`INSERT INTO data (item_id, timestamp, value) VALUES (?, ?, ?)`,
			4, s.ts, s.value); err != nil {
			t.Fatalf("insert data: %v", err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newProviderWithDB returns a Provider whose *sql.DB is the one we
// just seeded. It bypasses NewProvider so the test does not have to
// drop a real file on disk.
func newProviderWithDB(t *testing.T, db *sql.DB) *Provider {
	t.Helper()
	return &Provider{db: db, dsn: ":memory:"}
}

func TestNewProvider_MissingFile(t *testing.T) {
	p, err := NewProvider(filepath.Join(t.TempDir(), "does-not-exist.db"))
	if err != nil {
		t.Fatalf("NewProvider missing file should not error: %v", err)
	}
	if p.HasDatabase() {
		t.Fatalf("HasDatabase must be false for missing file")
	}
}

func TestNewProvider_EmptyPath(t *testing.T) {
	p, err := NewProvider("")
	if err != nil {
		t.Fatalf("NewProvider empty: %v", err)
	}
	if p.HasDatabase() {
		t.Fatalf("HasDatabase must be false for empty path")
	}
}

func TestItems_ReturnsSorted(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.Items(nil)
	if got, want := len(items), 4; got != want {
		t.Fatalf("len(items) = %d, want %d", got, want)
	}
	wantOrder := []string{
		"custom/boiler/FlameDuration",
		"custom/pump/status_1",
		"zigbee/aa/temperature",
		"zigbee/bb/temperature",
	}
	for i, it := range items {
		got := it.Endpoint + "/" + it.Property
		if got != wantOrder[i] {
			t.Errorf("item %d = %s, want %s", i, got, wantOrder[i])
		}
	}
}

func TestFindItems(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	// Prefix match on the endpoint.
	got := p.FindItems("zigbee", "temperature", nil)
	if len(got) != 2 {
		t.Fatalf("FindItems(zigbee, temperature) = %d, want 2", len(got))
	}
	// Exact match.
	got = p.FindItems("custom/boiler", "FlameDuration", nil)
	if len(got) != 1 {
		t.Fatalf("FindItems exact: %d, want 1", len(got))
	}
	// Wildcard endpoint.
	got = p.FindItems("", "temperature", nil)
	if len(got) != 2 {
		t.Fatalf("FindItems(, temperature) = %d, want 2", len(got))
	}
}

func TestQuery_AvgHour(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("zigbee/aa", "temperature", nil)
	stats, err := p.Query(context.Background(), QueryOptions{
		Items:  items,
		Metric: MetricAvg,
		Series: SeriesHour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}
	if stats[0].SampleSize != 24 {
		t.Errorf("SampleSize = %d, want 24", stats[0].SampleSize)
	}
	if stats[0].Avg == nil {
		t.Fatal("Avg should be non-nil")
	}
	// 20.0 + 0.0..2.3 / 10 = average of 20.0..22.3 step 0.1 в†’ 21.15
	if math.Abs(*stats[0].Avg-21.15) > 0.01 {
		t.Errorf("Avg = %f, want ~21.15", *stats[0].Avg)
	}
}

func TestQuery_MinMaxHour(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("zigbee/aa", "temperature", nil)
	for _, tc := range []struct {
		metric Metric
		want   float64
	}{
		{MetricMin, 19.0},
		// For h=23: 21.0 + 23/10 = 23.3
		{MetricMax, 23.3},
	} {
		stats, err := p.Query(context.Background(), QueryOptions{Items: items, Metric: tc.metric, Series: SeriesHour})
		if err != nil {
			t.Fatalf("%s: %v", tc.metric, err)
		}
		ptr := stats[0].Min
		if tc.metric == MetricMax {
			ptr = stats[0].Max
		}
		if ptr == nil {
			t.Fatalf("%s: nil pointer", tc.metric)
		}
		if math.Abs(*ptr-tc.want) > 0.01 {
			t.Errorf("%s = %f, want %f", tc.metric, *ptr, tc.want)
		}
	}
}

func TestQuery_SumFlame(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("custom/boiler", "FlameDuration", nil)
	stats, err := p.Query(context.Background(), QueryOptions{Items: items, Metric: MetricSum, Series: SeriesHour})
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].Sum == nil || math.Abs(*stats[0].Sum-300) > 0.01 {
		t.Errorf("Sum = %v, want 300", stats[0].Sum)
	}
}

func TestQuery_ExtremaData(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("zigbee/aa", "temperature", nil)
	stats, err := p.Query(context.Background(), QueryOptions{Items: items, Metric: MetricExtrema, Series: SeriesHour})
	if err != nil {
		t.Fatal(err)
	}
	s := stats[0]
	if s.Min == nil || s.Max == nil || s.Avg == nil || s.Sum == nil {
		t.Fatalf("extrema should populate all aggregates, got %+v", s)
	}
}

func TestQuery_TimeRange(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("zigbee/aa", "temperature", nil)
	// Restrict to first 5 hours of the seeded day.
	from := time.UnixMilli(dayStart)
	to := time.UnixMilli(dayStart + 5*3600*1000)
	stats, err := p.Query(context.Background(), QueryOptions{
		Items: items, Metric: MetricCount, Series: SeriesHour,
		From: from, To: to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats[0].SampleSize != 5 {
		t.Errorf("SampleSize in [from,to) = %d, want 5", stats[0].SampleSize)
	}
}

func TestQuery_Transitions(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("custom/pump", "status_1", nil)
	from := time.UnixMilli(dayStart - 600000)
	// "to" set far in the future so the open tail is counted.
	to := time.UnixMilli(dayStart + 10*3600*1000)
	trs, err := p.QueryTransitions(context.Background(), items, from, to, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(trs) != 1 {
		t.Fatalf("transitions = %d, want 1", len(trs))
	}
	tr := trs[0]
	// 6 numeric state changes (the [unavailable] row is ignored):
	// offв†’on, onв†’off, offв†’on, onв†’off, offв†’on, onв†’off.
	if tr.OffToOn != 3 || tr.OnToOff != 3 {
		t.Errorf("transitions = offв†’on:%d onв†’off:%d, want 3/3", tr.OffToOn, tr.OnToOff)
	}
	if tr.CurrentState != "off" {
		t.Errorf("CurrentState = %q, want off", tr.CurrentState)
	}
	if tr.OnSeconds == 0 || tr.OffSeconds == 0 {
		t.Errorf("expected non-zero on/off seconds, got on=%f off=%f", tr.OnSeconds, tr.OffSeconds)
	}
}

func TestQueryDaily(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	items := p.FindItems("zigbee/aa", "temperature", nil)
	// Range covers the seeded single day.
	from := time.UnixMilli(dayStart)
	to := time.UnixMilli(dayStart + 24*3600*1000)
	buckets, err := p.QueryDaily(context.Background(), QueryOptions{
		Items: items, Metric: MetricAvg, Series: SeriesHour,
		From: from, To: to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("daily buckets = %d, want 1", len(buckets))
	}
	if buckets[0].Count != 24 {
		t.Errorf("daily count = %d, want 24", buckets[0].Count)
	}
}

func TestParseBinaryState(t *testing.T) {
	cases := map[string]string{
		"on":            "on",
		"OFF":           "off",
		" true ":        "on",
		"0":             "off",
		"high":          "on",
		"closed":        "off",
		"[unavailable]": "",
		"":              "",
	}
	for in, want := range cases {
		if got := ParseBinaryState(in); got != want {
			t.Errorf("ParseBinaryState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveTimeRange(t *testing.T) {
	now := time.Date(2026, 6, 5, 13, 0, 0, 0, time.UTC)
	cases := []struct {
		from, to string
		wantF, wantT time.Time
		wantErr bool
	}{
		{"", "", time.Time{}, time.Time{}, false},
		{"today", "", time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), time.Time{}, false},
		{"yesterday", "today", time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), false},
		{"last-24h", "", now.Add(-24 * time.Hour), time.Time{}, false},
		{"2026-04-01", "2026-05-01", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), false},
		{"bogus", "", time.Time{}, time.Time{}, true},
	}
	for _, c := range cases {
		f, t2, err := ResolveTimeRange(c.from, c.to, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("ResolveTimeRange(%q, %q) expected error", c.from, c.to)
			}
			continue
		}
		if err != nil {
			t.Errorf("ResolveTimeRange(%q, %q): %v", c.from, c.to, err)
			continue
		}
		if !f.Equal(c.wantF) {
			t.Errorf("ResolveTimeRange(%q, %q) from = %s, want %s", c.from, c.to, f, c.wantF)
		}
		if !t2.Equal(c.wantT) {
			t.Errorf("ResolveTimeRange(%q, %q) to = %s, want %s", c.from, c.to, t2, c.wantT)
		}
	}
}

// fakeNameLookup is a hand-written NameLookup for tests; it returns
// a single match per (endpoint, property) pair to verify that
// enrichment propagates through Items().
type fakeNameLookup struct {
	items []NameMatch
}

func (f *fakeNameLookup) Lookup(endpoint, expose, property string) []NameMatch {
	var out []NameMatch
	for _, m := range f.items {
		if m.Endpoint == endpoint && m.Property == property {
			out = append(out, m)
		}
	}
	return out
}

func TestItems_WithLookup(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	lookup := &fakeNameLookup{items: []NameMatch{
		{Dashboard: "РљР»РёРјР°С‚", Block: "РЎРїР°Р»СЊРЅСЏ", Name: "РўРµРјРїРµСЂР°С‚СѓСЂР°", Endpoint: "zigbee/aa", Property: "temperature"},
	}}
	items := p.Items(lookup)
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	var found *AnnotatedItem
	for i := range items {
		if items[i].Endpoint == "zigbee/aa" && items[i].Property == "temperature" {
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatal("zigbee/aa/temperature not found")
	}
	if len(found.Usage) != 1 || found.Usage[0].Name != "РўРµРјРїРµСЂР°С‚СѓСЂР°" {
		t.Errorf("usage not enriched: %+v", found.Usage)
	}
}

// TestFindItems_EndpointAlias verifies that when an alias resolver
// is wired in, FindItems returns the rows matching the canonical
// id-form endpoint even when the caller passed the broker-side
// name form. The id-form (custom/boiler) is the only one stored in
// the recorder database; the name form (custom/РљРѕС‚С‘Р») is what the
// LLM sees in homed_list_devices output.
func TestFindItems_EndpointAlias(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	p.SetEndpointAliases(func(endpoint string) string {
		switch endpoint {
		case "custom/РљРѕС‚С‘Р»":
			return "custom/boiler"
		case "custom/РўС‘РїР»С‹Р№ РїРѕР»":
			return "custom/pump"
		}
		return ""
	})
	// Name-form query must now find the id-form rows.
	got := p.FindItems("custom/РљРѕС‚С‘Р»", "FlameDuration", nil)
	if len(got) != 1 || got[0].Endpoint != "custom/boiler" {
		t.Fatalf("name form: got %+v, want one row at custom/boiler", got)
	}
	got = p.FindItems("custom/РўС‘РїР»С‹Р№ РїРѕР»", "status_1", nil)
	if len(got) != 1 || got[0].Endpoint != "custom/pump" {
		t.Fatalf("name form 2: got %+v, want one row at custom/pump", got)
	}
	// A query for an unknown name must still return zero rows, not
	// false positives from other endpoints.
	if got := p.FindItems("custom/РќРµРёР·РІРµСЃС‚РЅРѕ", "FlameDuration", nil); len(got) != 0 {
		t.Errorf("unknown name: got %+v, want empty", got)
	}
	// Without a resolver, the name form returns zero rows.
	p.SetEndpointAliases(nil)
	if got := p.FindItems("custom/РљРѕС‚С‘Р»", "FlameDuration", nil); len(got) != 0 {
		t.Errorf("without resolver: got %+v, want empty", got)
	}
}

// TestItems_EndpointAliasEnrichment verifies that Items() enriches
// the id-form row with the user-defined name when the recorded row
// is the id form. The test seeds a NameLookup that responds to
// BOTH the id form and the name form (the alias resolver returns
// the id form from the name form), and expects the row to be
// enriched with the name-form match.
func TestItems_EndpointAliasEnrichment(t *testing.T) {
	p := newProviderWithDB(t, seedDB(t))
	p.SetEndpointAliases(func(endpoint string) string {
		// Bidirectional resolver: in production this map is
		// populated by the alias builder in main.go, which walks
		// the per-service status retain and adds both
		// nameв†’id and idв†’name entries.
		switch endpoint {
		case "custom/РљРѕС‚С‘Р»":
			return "custom/boiler"
		case "custom/boiler":
			return "custom/РљРѕС‚С‘Р»"
		}
		return ""
	})
	lookup := &fakeNameLookup{items: []NameMatch{
		{Dashboard: "РљРѕС‚РµР»", Block: "РљРѕС‚РµР»", Name: "РљРѕС‚С‘Р»", Endpoint: "custom/РљРѕС‚С‘Р»", Property: "FlameDuration"},
	}}
	items := p.Items(lookup)
	var found *AnnotatedItem
	for i := range items {
		if items[i].Endpoint == "custom/boiler" {
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatal("custom/boiler not found")
	}
	if len(found.Usage) != 1 || found.Usage[0].Name != "РљРѕС‚С‘Р»" {
		t.Errorf("id-form row not enriched with name-form match: %+v", found.Usage)
	}
}
