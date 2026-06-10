// Package recorder provides read-only access to the SQLite database
// written by homed-recorder. The database contains three tables:
//
//	CREATE TABLE item  (id INTEGER PRIMARY KEY, endpoint TEXT, property TEXT, debounce INTEGER, threshold REAL);
//	CREATE TABLE data  (id INTEGER PRIMARY KEY, item_id INTEGER, timestamp INTEGER, value TEXT);
//	CREATE TABLE hour  (id INTEGER PRIMARY KEY, item_id INTEGER, timestamp INTEGER, avg REAL, min REAL, max REAL);
//
// "hour" stores pre-aggregated hour buckets (avg/min/max) for numeric
// items; "data" holds raw samples вЂ” used both for numeric items that
// have not yet been bucketed and for discrete values (on/off,
// true/false, custom textual values).
//
// The package never writes to the database; it opens it in read-only
// mode and runs SELECT queries. A missing file is not an error: the
// Provider just behaves as if there is no recorded data.
package recorder

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/u236/homed-mcp/internal/logger"

	// Pure-Go SQLite driver вЂ” no CGO required, no toolchain deps.
	_ "modernc.org/sqlite"
)

// Item mirrors a row of the `item` table. Endpoint and property are
// the join keys with HOMEd's MQTT sub-topics; debounce and threshold
// are the recorder's own configuration for that item (exposed mainly
// for diagnostics).
type Item struct {
	ID        int     `json:"id"`
	Endpoint  string  `json:"endpoint"`
	Property  string  `json:"property"`
	Debounce  int     `json:"debounce"`
	Threshold float64 `json:"threshold"`
}

// Usage describes where the user has placed this item in homed-web
// (dashboard / block / user-defined name). It mirrors the shape
// produced by homedweb.Match.Item.
type Usage struct {
	Dashboard string `json:"dashboard,omitempty"`
	Block     string `json:"block,omitempty"`
	Name      string `json:"name,omitempty"`
	Expose    string `json:"expose,omitempty"`
	Property  string `json:"property,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
}

// AnnotatedItem joins a raw recorder item with every user-defined
// label that homed-web knows about it. Returned by Provider.Items.
type AnnotatedItem struct {
	Item
	Usage []Usage `json:"usage,omitempty"`
}

// Metric selects what kind of value to compute over the matched rows.
type Metric string

// Supported metrics. Unknown values are rejected at query time.
const (
	MetricAvg     Metric = "avg"     // average of "avg" (hour) / numeric value (data)
	MetricMin     Metric = "min"     // minimum of "min" (hour) / numeric value (data)
	MetricMax     Metric = "max"     // maximum of "max" (hour) / numeric value (data)
	MetricSum     Metric = "sum"     // sum of "avg" (hour) / numeric value (data)
	MetricCount   Metric = "count"   // number of rows (hour or data, depending on Series)
	MetricFirst   Metric = "first"   // first sample in range
	MetricLast    Metric = "last"    // last sample in range
	MetricExtrema Metric = "extrema" // min + max + avg + sum + count in one go
)

// Valid reports whether m is a known metric.
func (m Metric) Valid() bool {
	switch m {
	case MetricAvg, MetricMin, MetricMax, MetricSum,
		MetricCount, MetricFirst, MetricLast, MetricExtrema:
		return true
	}
	return false
}

// Series picks which table the metric is computed over.
type Series string

// Supported series.
const (
	SeriesHour Series = "hour" // use the hour table (pre-aggregated)
	SeriesData Series = "data" // use the raw data table
)

// Valid reports whether s is a known series.
func (s Series) Valid() bool {
	return s == SeriesHour || s == SeriesData
}

// Sample is a single timestamped value, used by the "first"/"last"
// metrics and by the raw dump produced by metric=count with a small
// limit. The Value field is preserved as a string so that the caller
// can see discrete states ("on", "off", "[unavailable]") verbatim.
type Sample struct {
	Timestamp time.Time `json:"timestamp"`
	Ms        int64     `json:"timestampMs"`
	Value     string    `json:"value"`
}

// Stat is the result of a single metric computation. Only the fields
// relevant for the chosen metric are populated. The other fields are
// left at their zero value.
type Stat struct {
	// Common metadata.
	Item       AnnotatedItem `json:"item"`
	Series     Series        `json:"series"`
	Metric     Metric        `json:"metric"`
	From       time.Time     `json:"from"`
	To         time.Time     `json:"to"`
	SampleSize int           `json:"sampleSize"` // number of source rows considered

	// Aggregate outputs. Numeric values use pointers so that "JSON
	// null" can be used to mean "not applicable" (e.g. Min for a
	// count metric).
	Avg   *float64 `json:"avg,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Sum   *float64 `json:"sum,omitempty"`
	Count *int64   `json:"count,omitempty"`

	// First/Last produce timestamped samples, not aggregates.
	First *Sample `json:"first,omitempty"`
	Last  *Sample `json:"last,omitempty"`
}

// QueryOptions controls a single Query call. All fields are optional
// except where noted. The zero value selects MetricAvg, SeriesHour,
// and an "all-time" range.
type QueryOptions struct {
	Items  []AnnotatedItem // restrict to these items; nil = all
	Metric Metric           // see Metric* constants
	Series Series           // see Series* constants
	From   time.Time        // inclusive; zero = no lower bound
	To     time.Time        // exclusive; zero = no upper bound
}

// EndpointAliasResolver maps a broker-side name endpoint to its
// canonical id-based form (or vice-versa) and is consulted by
// FindItems / Items so that:
//
//   - a query issued with the user-visible name form (e.g.
//     "custom/OpenTherm", which is what the LLM sees in
//     homed_list_devices output) still finds the rows written by
//     the recorder, which key on the canonical id form
//     (e.g. "custom/61226326-10251872");
//   - a row stored in the recorder under the id form can be
//     enriched with the user-defined name that homed-web has for
//     the name form, so the LLM sees the friendly label and the
//     id side-by-side.
//
// The resolver is invoked with one endpoint and returns the
// "other" form, or the input unchanged when no aliasing is known.
// A bidirectional map is the natural implementation. The interface
// is duplicated here rather than imported from the homedweb
// package to keep the recorder package free of higher-level
// dependencies.
type EndpointAliasResolver func(endpoint string) string

// Provider is a thread-safe wrapper around a read-only SQLite handle
// pointing at a homed-recorder database. A zero Provider is valid and
// behaves as "no database loaded".
type Provider struct {
	mu     sync.RWMutex
	db     *sql.DB
	dsn    string
	log    *logger.Logger
	alias  EndpointAliasResolver
	aliasMu sync.RWMutex
}

// SetEndpointAliases wires a name<->id alias resolver into the
// provider. When set, FindItems and Items treat an endpoint argument
// as the name form first, then look up the canonical id form and
// return rows for both. A nil resolver disables the aliasing
// behaviour (the default). The call is safe to invoke at any time
// after construction and from multiple goroutines.
func (p *Provider) SetEndpointAliases(resolver EndpointAliasResolver) {
	p.aliasMu.Lock()
	defer p.aliasMu.Unlock()
	p.alias = resolver
}

// resolveAlias returns the canonical id-based endpoint for the given
// (possibly name-based) endpoint, or the input unchanged when no
// alias is configured or the resolver has no mapping for it.
func (p *Provider) resolveAlias(endpoint string) string {
	if endpoint == "" {
		return endpoint
	}
	p.aliasMu.RLock()
	resolver := p.alias
	p.aliasMu.RUnlock()
	if resolver == nil {
		return endpoint
	}
	if resolved := resolver(endpoint); resolved != "" && resolved != endpoint {
		return resolved
	}
	return endpoint
}

// NewProvider opens the SQLite database at path in read-only mode.
// A missing file is not an error вЂ” it just yields a Provider that
// reports HasDatabase() == false. Other I/O / format errors are
// surfaced so the operator notices a misconfigured path.
//
// This is a thin wrapper around NewProviderWithLogger(path, nil) kept
// for backward compatibility with code that does not care about
// structured logging.
func NewProvider(path string) (*Provider, error) {
	return NewProviderWithLogger(path, nil)
}

// NewProviderWithLogger is like NewProvider but also wires a
// structured logger used to dump the contents of the database at
// start-up (counts of items/data rows, range of timestamps, ...) on
// info, and a JSON dump of every item row on debug. A nil logger is
// allowed; logging is then simply disabled.
func NewProviderWithLogger(path string, log *logger.Logger) (*Provider, error) {
	p := &Provider{log: log}
	if strings.TrimSpace(path) == "" {
		return p, nil
	}
	// "file:" + "mode=ro" вЂ” modernc.org/sqlite accepts URL-style
	// DSNs. mode=ro makes it impossible to accidentally write to
	// the recorder's database.
	dsn := "file:" + path + "?mode=ro"
	if p.log != nil {
		p.log.Infof("recorder: opening %s in read-only mode", path)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		if p.log != nil {
			p.log.Infof("recorder: open %s: %s", path, err)
		}
		return p, fmt.Errorf("recorder: open %s: %w", path, err)
	}
	// Verify the connection. Missing file в†’ no error (treated as
	// "no database"), other errors are surfaced.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		// Translate missing-file conditions into a silent no-op.
		msg := err.Error()
		if strings.Contains(msg, "unable to open") ||
			strings.Contains(msg, "no such file") ||
			strings.Contains(msg, "not a database") ||
			errors.Is(err, sql.ErrConnDone) {
			if p.log != nil {
				p.log.Infof("recorder: %s is missing or empty, continuing without historical data", path)
			}
			return p, nil
		}
		if p.log != nil {
			p.log.Infof("recorder: ping %s: %s", path, err)
		}
		return p, fmt.Errorf("recorder: ping %s: %w", path, err)
	}
	p.db = db
	p.dsn = path
	// Dump the table statistics at start-up so that the operator
	// can tell at a glance what range of data is available.
	p.logInventory()
	return p, nil
}

// logInventory reads a few diagnostic counts from the just-opened
// recorder database and writes them to the configured logger. The
// function is a no-op when no logger is wired or the database is
// missing. Each query is wrapped in its own error-handling branch
// so a single broken table does not prevent the rest from being
// reported.
func (p *Provider) logInventory() {
	if p.log == nil || p.db == nil {
		return
	}
	var items, dataRows, hourRows int64
	if err := p.db.QueryRow("SELECT COUNT(*) FROM item").Scan(&items); err != nil {
		p.log.Infof("recorder: count item: %s", err)
	}
	if err := p.db.QueryRow("SELECT COUNT(*) FROM data").Scan(&dataRows); err != nil {
		p.log.Infof("recorder: count data: %s", err)
	}
	if err := p.db.QueryRow("SELECT COUNT(*) FROM hour").Scan(&hourRows); err != nil {
		p.log.Infof("recorder: count hour: %s", err)
	}
	var (
		firstDataMs, lastDataMs int64
		firstHourMs, lastHourMs int64
	)
	if dataRows > 0 {
		if err := p.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM data").Scan(&firstDataMs, &lastDataMs); err != nil {
			p.log.Infof("recorder: range data: %s", err)
		}
	}
	if hourRows > 0 {
		if err := p.db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM hour").Scan(&firstHourMs, &lastHourMs); err != nil {
			p.log.Infof("recorder: range hour: %s", err)
		}
	}
	p.log.Infof("recorder: loaded %s (items=%d, data=%d, hour=%d)",
		p.dsn, items, dataRows, hourRows)
	if dataRows > 0 {
		p.log.Infof("recorder: data range: %s .. %s (%d rows)",
			time.UnixMilli(firstDataMs).UTC().Format(time.RFC3339),
			time.UnixMilli(lastDataMs).UTC().Format(time.RFC3339),
			dataRows)
	}
	if hourRows > 0 {
		p.log.Infof("recorder: hour range: %s .. %s (%d rows)",
			time.UnixMilli(firstHourMs).UTC().Format(time.RFC3339),
			time.UnixMilli(lastHourMs).UTC().Format(time.RFC3339),
			hourRows)
	}
	if items > 0 {
		rows, err := p.db.Query("SELECT id, endpoint, property, debounce, threshold FROM item")
		if err != nil {
			p.log.Infof("recorder: dump item: %s", err)
			return
		}
		defer rows.Close()
		var dumped int
		for rows.Next() {
			var it Item
			if err := rows.Scan(&it.ID, &it.Endpoint, &it.Property, &it.Debounce, &it.Threshold); err != nil {
				p.log.Infof("recorder: scan item: %s", err)
				continue
			}
			p.log.Debugf("recorder: item id=%d endpoint=%s property=%s debounce=%d threshold=%g",
				it.ID, it.Endpoint, it.Property, it.Debounce, it.Threshold)
			dumped++
		}
		if err := rows.Err(); err != nil {
			p.log.Infof("recorder: iterate item: %s", err)
		}
	}
}

// HasDatabase reports whether a SQLite handle is currently held.
func (p *Provider) HasDatabase() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.db != nil
}

// DSN returns the path the provider was opened with. Useful for log
// messages; empty when no database is loaded.
func (p *Provider) DSN() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dsn
}

// Close releases the underlying SQLite handle. Safe to call on a
// zero Provider.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db == nil {
		return nil
	}
	err := p.db.Close()
	p.db = nil
	return err
}

// loadItems reads every row of the `item` table into memory. The
// result is sorted by (endpoint, property, id) for stable output.
func (p *Provider) loadItems() ([]Item, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query("SELECT id, endpoint, property, debounce, threshold FROM item")
	if err != nil {
		return nil, fmt.Errorf("recorder: select item: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.Endpoint, &it.Property, &it.Debounce, &it.Threshold); err != nil {
			return nil, fmt.Errorf("recorder: scan item: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorder: iterate item: %w", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Endpoint != out[j].Endpoint {
			return out[i].Endpoint < out[j].Endpoint
		}
		if out[i].Property != out[j].Property {
			return out[i].Property < out[j].Property
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// NameLookup is the slim subset of homedweb.Provider that the
// recorder package needs. It is defined here (not imported) so the
// package depends only on the standard library + the sqlite driver.
// Callers can pass *homedweb.Provider directly; the struct satisfies
// the interface structurally (no extra methods on the provider).
type NameLookup interface {
	Lookup(endpoint, expose, property string) []NameMatch
}

// NameMatch is the slim subset of homedweb.Match that the recorder
// package reads. homedweb.Match is a struct with exported fields, so
// the compiler does NOT see it as implementing this interface вЂ” the
// recorder package will accept a small adapter instead (see
// AdapterForHomedweb). This keeps the import graph free of circular
// references and makes the package trivially testable with a fake.
type NameMatch struct {
	Dashboard string
	Block     string
	Name      string
	Expose    string
	Property  string
	Endpoint  string
}

// Items returns every item, optionally enriched with user-defined
// names from the supplied NameLookup. When lookup is nil, no
// enrichment is performed. The result is sorted by
// (endpoint, property, id).
func (p *Provider) Items(lookup NameLookup) []AnnotatedItem {
	raw, err := p.loadItems()
	if err != nil || len(raw) == 0 {
		return nil
	}
	out := make([]AnnotatedItem, 0, len(raw))
	for _, it := range raw {
		ai := AnnotatedItem{Item: it}
		if lookup != nil {
			// Enrich the row with the user-defined name(s) for
			// both the recorded id form and its name-form alias
			// (when the service runs with names=true). Doing
			// both lookups costs one extra hash lookup per row
			// and lets the LLM see the friendly name and the
			// id side-by-side, even when the user asked about
			// the name form.
			for _, ep := range p.aliasEndpoints(it.Endpoint) {
				for _, m := range lookup.Lookup(ep, "", it.Property) {
					ai.Usage = append(ai.Usage, Usage{
						Dashboard: m.Dashboard,
						Block:     m.Block,
						Name:      m.Name,
						Expose:    m.Expose,
						Property:  m.Property,
						Endpoint:  m.Endpoint,
					})
				}
			}
		}
		out = append(out, ai)
	}
	return out
}

// aliasEndpoints returns the list of endpoint forms (the original
// one plus, when configured, the canonical id form resolved by the
// alias resolver) that should be looked up for a given endpoint
// string. The order is preserved and duplicates are removed so
// that callers can iterate without re-checking.
//
// The resolver is consulted in both directions: when the recorded
// row carries the id form, the resolver is asked for the
// corresponding name form, so the lookup against homed-web's
// user-defined names can match on the friendly name. When the
// recorded row carries the name form, the resolver returns the id
// form. Callers iterate the resulting list and try the lookup on
// every form; the first hit wins (and they all get merged into a
// single Usage slice).
func (p *Provider) aliasEndpoints(endpoint string) []string {
	if endpoint == "" {
		return []string{endpoint}
	}
	resolved := p.resolveAlias(endpoint)
	if resolved == endpoint || resolved == "" {
		return []string{endpoint}
	}
	return []string{endpoint, resolved}
}

// FindItems returns the items whose endpoint/property pair matches
// the given patterns. An empty pattern is treated as a wildcard
// ("anything"). The endpoint pattern matches by prefix вЂ” passing
// "zigbee" returns every item whose endpoint starts with "zigbee/"
// (and "zigbee/" itself is allowed as a literal exact match). The
// property pattern is an exact, case-sensitive match. The lookup
// argument is used for enrichment (it can be nil).
//
// When an alias resolver is wired in, the endpoint argument is
// treated as the broker-side name form first: if the resolver maps
// it to a canonical id (e.g. "custom/OpenTherm" в†’ "custom/6122вЂ¦"),
// rows matching EITHER form are returned. This is the only way the
// recorder, which keys on the id form, can answer a question asked
// with the name form (which is what the LLM sees in
// homed_list_devices output).
func (p *Provider) FindItems(endpoint, property string, lookup NameLookup) []AnnotatedItem {
	all := p.Items(lookup)
	if endpoint == "" && property == "" {
		return all
	}
	// Expand the endpoint pattern with its alias counterpart. The
	// loop below matches against any form in the set.
	patterns := []string{endpoint}
	if endpoint != "" {
		if resolved := p.resolveAlias(endpoint); resolved != "" && resolved != endpoint {
			patterns = append(patterns, resolved)
		}
	}
	out := make([]AnnotatedItem, 0, len(all))
	for _, it := range all {
		if !endpointMatchesAny(it.Endpoint, patterns) {
			continue
		}
		if property != "" && it.Property != property {
			continue
		}
		out = append(out, it)
	}
	return out
}

// endpointMatchesAny returns true when endpoint matches any of the
// supplied patterns. The matching rules mirror the original
// FindItems: an empty pattern matches everything; a pattern ending
// in "/" is a prefix; any other pattern is a literal exact match OR
// a prefix-match (treating "custom" as "custom/...").
func endpointMatchesAny(endpoint string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			return true
		}
		var prefix string
		switch {
		case strings.HasSuffix(p, "/"):
			prefix = p
		default:
			prefix = p + "/"
		}
		if endpoint == p || strings.HasPrefix(endpoint, prefix) {
			return true
		}
	}
	return false
}

// Query runs the requested metric/series over the given items and
// the given time range. The returned slice has one Stat per item,
// in the same order as opts.Items. Items that have no matching rows
// are still returned, with a zero-value Stat and SampleSize = 0.
func (p *Provider) Query(ctx context.Context, opts QueryOptions) ([]Stat, error) {
	if !opts.Series.Valid() {
		return nil, fmt.Errorf("recorder: unknown series %q (want hour or data)", opts.Series)
	}
	if !opts.Metric.Valid() {
		return nil, fmt.Errorf("recorder: unknown metric %q (want one of avg/min/max/sum/count/first/last/extrema)", opts.Metric)
	}
	items := opts.Items
	if items == nil {
		items = p.Items(nil)
	}
	out := make([]Stat, 0, len(items))
	for _, ai := range items {
		s, err := p.queryOne(ctx, ai, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (p *Provider) queryOne(ctx context.Context, ai AnnotatedItem, opts QueryOptions) (Stat, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	stat := Stat{
		Item:   ai,
		Series: opts.Series,
		Metric: opts.Metric,
		From:   opts.From,
		To:     opts.To,
	}
	if db == nil {
		return stat, nil
	}
	var fromMs, toMs int64
	if !opts.From.IsZero() {
		fromMs = opts.From.UnixMilli()
	}
	if !opts.To.IsZero() {
		toMs = opts.To.UnixMilli()
	}
	switch opts.Series {
	case SeriesHour:
		return stat, p.queryHour(ctx, db, ai.ID, fromMs, toMs, opts, &stat)
	case SeriesData:
		return stat, p.queryData(ctx, db, ai.ID, fromMs, toMs, opts, &stat)
	}
	return stat, nil
}

func (p *Provider) queryHour(ctx context.Context, db *sql.DB, itemID int, fromMs, toMs int64, opts QueryOptions, stat *Stat) error {
	where := []string{"item_id = ?"}
	args := []any{itemID}
	if fromMs != 0 {
		where = append(where, "timestamp >= ?")
		args = append(args, fromMs)
	}
	if toMs != 0 {
		where = append(where, "timestamp < ?")
		args = append(args, toMs)
	}
	clause := strings.Join(where, " AND ")

	switch opts.Metric {
	case MetricCount:
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM hour WHERE "+clause, args...).Scan(&n); err != nil {
			return fmt.Errorf("recorder: count hour: %w", err)
		}
		stat.SampleSize = int(n)
		stat.Count = &n
		return nil

	case MetricFirst:
		var ts int64
		var a, mi, ma float64
		row := db.QueryRowContext(ctx, "SELECT timestamp, avg, min, max FROM hour WHERE "+clause+" ORDER BY timestamp ASC LIMIT 1", args...)
		if err := row.Scan(&ts, &a, &mi, &ma); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("recorder: first hour: %w", err)
		}
		stat.SampleSize = 1
		stat.First = &Sample{
			Timestamp: time.UnixMilli(ts).UTC(),
			Ms:        ts,
			Value:     fmt.Sprintf("avg=%.4f min=%.4f max=%.4f", a, mi, ma),
		}
		return nil

	case MetricLast:
		var ts int64
		var a, mi, ma float64
		row := db.QueryRowContext(ctx, "SELECT timestamp, avg, min, max FROM hour WHERE "+clause+" ORDER BY timestamp DESC LIMIT 1", args...)
		if err := row.Scan(&ts, &a, &mi, &ma); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("recorder: last hour: %w", err)
		}
		stat.SampleSize = 1
		stat.Last = &Sample{
			Timestamp: time.UnixMilli(ts).UTC(),
			Ms:        ts,
			Value:     fmt.Sprintf("avg=%.4f min=%.4f max=%.4f", a, mi, ma),
		}
		return nil
	}

	// Aggregate metrics: avg / min / max / sum / extrema.
	selects := []string{
		"COUNT(*)",
		"COALESCE(AVG(avg), 0)",
		"COALESCE(MIN(min), 0)",
		"COALESCE(MAX(max), 0)",
		"COALESCE(SUM(avg), 0)",
	}
	row := db.QueryRowContext(ctx, "SELECT "+strings.Join(selects, ", ")+" FROM hour WHERE "+clause, args...)
	var (
		n    int64
		avgV float64
		minV float64
		maxV float64
		sumV float64
	)
	if err := row.Scan(&n, &avgV, &minV, &maxV, &sumV); err != nil {
		return fmt.Errorf("recorder: aggregate hour: %w", err)
	}
	stat.SampleSize = int(n)
	if n == 0 {
		return nil
	}
	stat.Count = &n
	switch opts.Metric {
	case MetricAvg:
		stat.Avg = &avgV
	case MetricMin:
		stat.Min = &minV
	case MetricMax:
		stat.Max = &maxV
	case MetricSum:
		stat.Sum = &sumV
	case MetricExtrema:
		stat.Min = &minV
		stat.Max = &maxV
		stat.Avg = &avgV
		stat.Sum = &sumV
	}
	return nil
}

func (p *Provider) queryData(ctx context.Context, db *sql.DB, itemID int, fromMs, toMs int64, opts QueryOptions, stat *Stat) error {
	where := []string{"item_id = ?"}
	args := []any{itemID}
	if fromMs != 0 {
		where = append(where, "timestamp >= ?")
		args = append(args, fromMs)
	}
	if toMs != 0 {
		where = append(where, "timestamp < ?")
		args = append(args, toMs)
	}
	clause := strings.Join(where, " AND ")

	switch opts.Metric {
	case MetricCount:
		var n int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM data WHERE "+clause, args...).Scan(&n); err != nil {
			return fmt.Errorf("recorder: count data: %w", err)
		}
		stat.SampleSize = int(n)
		stat.Count = &n
		return nil

	case MetricFirst:
		var ts int64
		var v string
		row := db.QueryRowContext(ctx, "SELECT timestamp, value FROM data WHERE "+clause+" ORDER BY timestamp ASC LIMIT 1", args...)
		if err := row.Scan(&ts, &v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("recorder: first data: %w", err)
		}
		stat.SampleSize = 1
		stat.First = &Sample{
			Timestamp: time.UnixMilli(ts).UTC(),
			Ms:        ts,
			Value:     v,
		}
		return nil

	case MetricLast:
		var ts int64
		var v string
		row := db.QueryRowContext(ctx, "SELECT timestamp, value FROM data WHERE "+clause+" ORDER BY timestamp DESC LIMIT 1", args...)
		if err := row.Scan(&ts, &v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("recorder: last data: %w", err)
		}
		stat.SampleSize = 1
		stat.Last = &Sample{
			Timestamp: time.UnixMilli(ts).UTC(),
			Ms:        ts,
			Value:     v,
		}
		return nil
	}

	// Aggregate metrics over numeric values. Non-numeric values
	// (e.g. "on", "off", "[unavailable]") are excluded by the GLOB
	// predicate that requires at least one digit.
	selects := []string{
		"COUNT(*)",
		"COUNT(CASE WHEN value GLOB '*[0-9]*' THEN 1 END)",
		"COALESCE(AVG(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END), 0)",
		"COALESCE(MIN(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END), 0)",
		"COALESCE(MAX(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END), 0)",
		"COALESCE(SUM(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END), 0)",
	}
	row := db.QueryRowContext(ctx, "SELECT "+strings.Join(selects, ", ")+" FROM data WHERE "+clause, args...)
	var (
		n    int64
		nNum int64
		avgV float64
		minV float64
		maxV float64
		sumV float64
	)
	if err := row.Scan(&n, &nNum, &avgV, &minV, &maxV, &sumV); err != nil {
		return fmt.Errorf("recorder: aggregate data: %w", err)
	}
	stat.SampleSize = int(n)
	if nNum == 0 {
		// Return the total row count so the caller can tell
		// "no data at all" from "only non-numeric data".
		stat.Count = &n
		return nil
	}
	stat.Count = &nNum
	switch opts.Metric {
	case MetricAvg:
		stat.Avg = &avgV
	case MetricMin:
		stat.Min = &minV
	case MetricMax:
		stat.Max = &maxV
	case MetricSum:
		stat.Sum = &sumV
	case MetricExtrema:
		stat.Min = &minV
		stat.Max = &maxV
		stat.Avg = &avgV
		stat.Sum = &sumV
	}
	return nil
}

// DailyBucket is a per-day rollup produced by QueryDaily.
type DailyBucket struct {
	Day   string  `json:"day"`   // YYYY-MM-DD (UTC)
	Count int     `json:"count"` // rows that contributed
	Avg   float64 `json:"avg,omitempty"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Sum   float64 `json:"sum,omitempty"`
}

// QueryDaily returns one bucket per UTC day in [from, to). Useful
// for "the coldest day in April" style questions. The metric is
// applied per (item, day) pair and then reduced across items: for
// min/max the result is the extreme across items, for avg/sum the
// values are summed. Pass exactly one item when you want the
// per-item value back verbatim.
func (p *Provider) QueryDaily(ctx context.Context, opts QueryOptions) ([]DailyBucket, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	if db == nil {
		return nil, nil
	}
	if !opts.Series.Valid() {
		return nil, fmt.Errorf("recorder: unknown series %q", opts.Series)
	}
	if !opts.Metric.Valid() ||
		opts.Metric == MetricCount || opts.Metric == MetricFirst || opts.Metric == MetricLast {
		return nil, fmt.Errorf("recorder: daily rollup requires an aggregate metric (avg/min/max/sum)")
	}
	items := opts.Items
	if items == nil {
		items = p.Items(nil)
	}
	if len(items) == 0 {
		return nil, nil
	}
	var fromMs, toMs int64
	if !opts.From.IsZero() {
		fromMs = opts.From.UnixMilli()
	}
	if !opts.To.IsZero() {
		toMs = opts.To.UnixMilli()
	}

	// Build IN(...) clause for item ids.
	ids := make([]any, 0, len(items))
	placeholders := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
		placeholders = append(placeholders, "?")
	}
	idClause := strings.Join(placeholders, ",")

	table := string(opts.Series)
	// Build the per-day aggregate expression. For hour we use the
	// already-aggregated min/max columns; for data we CAST text в†’
	// REAL (skipping non-numeric values).
	var valueExpr string
	switch opts.Series {
	case SeriesHour:
		switch opts.Metric {
		case MetricMin:
			valueExpr = "MIN(min)"
		case MetricMax:
			valueExpr = "MAX(max)"
		case MetricSum:
			valueExpr = "SUM(avg)"
		default:
			valueExpr = "AVG(avg)"
		}
	case SeriesData:
		switch opts.Metric {
		case MetricMin:
			valueExpr = "MIN(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END)"
		case MetricMax:
			valueExpr = "MAX(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END)"
		case MetricSum:
			valueExpr = "SUM(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END)"
		default:
			valueExpr = "AVG(CASE WHEN value GLOB '*[0-9]*' THEN CAST(value AS REAL) END)"
		}
	}

	args := append([]any{}, ids...)
	where := []string{"item_id IN (" + idClause + ")"}
	if fromMs != 0 {
		where = append(where, "timestamp >= ?")
		args = append(args, fromMs)
	}
	if toMs != 0 {
		where = append(where, "timestamp < ?")
		args = append(args, toMs)
	}
	clause := strings.Join(where, " AND ")

	// One query that produces the day bucket + count + the per-day
	// metric in a single GROUP BY pass. The day bucket is the
	// UTC-aligned start of the day containing the timestamp.
	q := fmt.Sprintf(
		"SELECT (timestamp / 86400000) * 86400000 AS dayMs, COUNT(*), "+
			"COALESCE(%s, 0) FROM %s WHERE %s GROUP BY dayMs ORDER BY dayMs ASC",
		valueExpr, table, clause,
	)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("recorder: daily rollup: %w", err)
	}
	defer rows.Close()
	out := make([]DailyBucket, 0)
	for rows.Next() {
		var (
			dayMs int64
			count int
			val   float64
		)
		if err := rows.Scan(&dayMs, &count, &val); err != nil {
			return nil, fmt.Errorf("recorder: scan daily: %w", err)
		}
		b := DailyBucket{
			Day:   time.UnixMilli(dayMs).UTC().Format("2006-01-02"),
			Count: count,
		}
		switch opts.Metric {
		case MetricMin:
			b.Min = val
		case MetricMax:
			b.Max = val
		case MetricSum:
			b.Sum = val
		default:
			b.Avg = val
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recorder: iterate daily: %w", err)
	}
	return out, nil
}

// Transitions is a summary of offв†’on / onв†’off events for a binary
// item in [from, to). Discrete values like "on", "true", "1",
// "high", "open", "present" are counted as "on"; "off", "false",
// "0", "low", "close", "absent" as "off". Unknown values are
// skipped (e.g. "[unavailable]").
type Transitions struct {
	Item           AnnotatedItem `json:"item"`
	From           time.Time     `json:"from"`
	To             time.Time     `json:"to"`
	OnToOff        int           `json:"onToOff"`
	OffToOn        int           `json:"offToOn"`
	Total          int           `json:"total"`
	OnSeconds      float64       `json:"onSeconds"`
	OffSeconds     float64       `json:"offSeconds"`
	Coverage       float64       `json:"coverage"`
	CurrentState   string        `json:"currentState"`
	TransitionsLog []Sample      `json:"transitions,omitempty"`
}

// QueryTransitions counts on/off transitions in the data table for
// the given items. The "to" timestamp is treated as "now" for the
// purpose of attributing the open tail to the current state; pass
// the current time if you want the count to include the time since
// the last sample.
func (p *Provider) QueryTransitions(ctx context.Context, items []AnnotatedItem, from, to time.Time, limit int) ([]Transitions, error) {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	if db == nil {
		return nil, nil
	}
	if limit < 0 {
		limit = 0
	}
	if items == nil {
		items = p.Items(nil)
	}
	var fromMs, toMs int64
	if !from.IsZero() {
		fromMs = from.UnixMilli()
	}
	if !to.IsZero() {
		toMs = to.UnixMilli()
	}

	out := make([]Transitions, 0, len(items))
	for _, ai := range items {
		where := []string{"item_id = ?"}
		args := []any{ai.ID}
		if fromMs != 0 {
			where = append(where, "timestamp >= ?")
			args = append(args, fromMs)
		}
		if toMs != 0 {
			where = append(where, "timestamp < ?")
			args = append(args, toMs)
		}
		clause := strings.Join(where, " AND ")

		rows, err := db.QueryContext(ctx, "SELECT timestamp, value FROM data WHERE "+clause+" ORDER BY timestamp ASC", args...)
		if err != nil {
			return nil, fmt.Errorf("recorder: select transitions: %w", err)
		}
		var (
			samples    []Sample
			prev       string
			prevMs     int64
			havePrev   bool
			onSec      float64
			offSec     float64
			onToOff    int
			offToOn    int
			current    string
			currentMs  int64
			currentSet bool
			logCap     = limit
		)
		for rows.Next() {
			var ts int64
			var v string
			if err := rows.Scan(&ts, &v); err != nil {
				rows.Close()
				return nil, fmt.Errorf("recorder: scan transitions: %w", err)
			}
			state := ParseBinaryState(v)
			if state == "" {
				continue
			}
			if havePrev && prev != "" {
				dur := float64(ts-prevMs) / 1000.0
				if dur < 0 {
					dur = 0
				}
				switch prev {
				case "on":
					onSec += dur
				case "off":
					offSec += dur
				}
				if state != prev {
					switch {
					case prev == "off" && state == "on":
						offToOn++
					case prev == "on" && state == "off":
						onToOff++
					}
					if logCap > 0 {
						samples = append(samples, Sample{
							Timestamp: time.UnixMilli(ts).UTC(),
							Ms:        ts,
							Value:     prev + "в†’" + state,
						})
						logCap--
					}
				}
			}
			prev = state
			prevMs = ts
			havePrev = true
			current = state
			currentMs = ts
			currentSet = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("recorder: iterate transitions: %w", err)
		}
		tr := Transitions{
			Item:           ai,
			From:           from,
			To:             to,
			OnToOff:        onToOff,
			OffToOn:        offToOn,
			Total:          onToOff + offToOn,
			OnSeconds:      onSec,
			OffSeconds:     offSec,
			TransitionsLog: samples,
			CurrentState:   current,
		}
		// Attribute the open tail to the current state when "to" is
		// in the future (i.e. the most recent state is still
		// ongoing).
		if currentSet && toMs > currentMs {
			open := float64(toMs-currentMs) / 1000.0
			if open > 0 {
				switch current {
				case "on":
					tr.OnSeconds += open
				case "off":
					tr.OffSeconds += open
				}
			}
		}
		tr.Coverage = tr.OnSeconds + tr.OffSeconds
		out = append(out, tr)
	}
	return out, nil
}

// ParseBinaryState maps a free-form value string to one of "on",
// "off" or "" (unknown). Exported so that callers building their own
// summaries can apply the same rules.
func ParseBinaryState(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "1", "high", "open", "present", "yes":
		return "on"
	case "off", "false", "0", "low", "close", "closed", "absent", "no":
		return "off"
	}
	return ""
}

// ResolveTimeRange parses a textual time range. The two inputs are
// the "from" and "to" strings; either or both may be empty (meaning
// "no bound"). Recognised keywords:
//
//	today, yesterday, this-week, this-month
//	last-24h / last-1d, last-7d / last-week, last-30d / last-month
//	now
//	RFC3339 timestamp   (e.g. 2026-04-01T00:00:00Z)
//	YYYY-MM-DD          (interpreted as midnight UTC)
//
// The returned timestamps are always in UTC.
func ResolveTimeRange(from, to string, now time.Time) (time.Time, time.Time, error) {
	resolve := func(s string) (time.Time, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}, nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC(), nil
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.UTC(), nil
		}
		loc := now.Location()
		switch strings.ToLower(s) {
		case "now":
			return now.UTC(), nil
		case "today":
			y, m, d := now.In(loc).Date()
			return time.Date(y, m, d, 0, 0, 0, 0, loc).UTC(), nil
		case "yesterday":
			y, m, d := now.In(loc).AddDate(0, 0, -1).Date()
			return time.Date(y, m, d, 0, 0, 0, 0, loc).UTC(), nil
		case "this-week":
			wd := int(now.In(loc).Weekday())
			if wd == 0 {
				wd = 7
			}
			monday := now.In(loc).AddDate(0, 0, -(wd - 1))
			y, m, d := monday.Date()
			return time.Date(y, m, d, 0, 0, 0, 0, loc).UTC(), nil
		case "this-month":
			y, m, _ := now.In(loc).Date()
			return time.Date(y, m, 1, 0, 0, 0, 0, loc).UTC(), nil
		case "last-24h", "last-1d":
			return now.Add(-24 * time.Hour).UTC(), nil
		case "last-7d", "last-week":
			return now.Add(-7 * 24 * time.Hour).UTC(), nil
		case "last-30d", "last-month":
			return now.Add(-30 * 24 * time.Hour).UTC(), nil
		}
		return time.Time{}, fmt.Errorf("recorder: cannot parse time %q", s)
	}
	f, err := resolve(from)
	if err != nil {
		return f, f, err
	}
	t, err := resolve(to)
	if err != nil {
		return f, t, err
	}
	return f, t, nil
}