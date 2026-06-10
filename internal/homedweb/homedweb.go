// Package homedweb parses the database.json file written by homed-web
// and exposes the user-defined device / property names so that MCP
// tools can enrich their output with friendly labels.
//
// The file is a JSON object with the following shape (only the parts
// relevant for the MCP server are documented):
//
//	{
//	  "dashboards": [
//	    {
//	      "name": "РћС„РёСЃ",
//	      "blocks": [
//	        {
//	          "name": "РєРѕС‚РµР»",
//	          "items": [
//	            {"endpoint": "custom/61226326-10251872",
//	             "expose":   "OTget25",
//	             "name":     "РџРѕРґР°С‡Р°"}
//	          ]
//	        }
//	      ]
//	    }
//	  ],
//	  "names": {
//	    "custom/14705744-45074752/status_2": "рџљ°Р“Р’РЎ"
//	  }
//	}
//
// Parsing is permissive: missing or malformed files are not fatal; the
// caller can simply proceed without user-defined names. Callers should
// keep the returned Provider alive for the whole process lifetime and
// share it between MCP tools through the MetaSource interface.
package homedweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/u236/homed-mcp/internal/logger"
)

// Item is a single entry of a dashboard block. It references a device
// endpoint and either an expose key (named "expose") or a status
// property (named "property"). The Name field, if non-empty, is the
// user-friendly label the user has given this element.
type Item struct {
	Endpoint string `json:"endpoint"`
	Expose   string `json:"expose,omitempty"`
	Property string `json:"property,omitempty"`
	Name     string `json:"name,omitempty"`
}

// Block is a group of items inside a dashboard. The Name field is
// typically the room or sub-system label ("РљР»РёРјР°С‚", "Р’РѕРґРѕРїСЂРѕРІРѕРґ", ...).
type Block struct {
	Name  string `json:"name"`
	Items []Item `json:"items"`
}

// Dashboard groups several blocks under a single label (often a
// floor or functional category such as "РЎРІРµС‚", "РљРѕС‚РµР»", "Р’РѕРґР°").
type Dashboard struct {
	Name   string  `json:"name"`
	Blocks []Block `json:"blocks"`
}

// Database is the in-memory representation of homed-web's database.json.
type Database struct {
	Dashboards []Dashboard       `json:"dashboards"`
	Names      map[string]string `json:"names,omitempty"`
	Version    string            `json:"version,omitempty"`
	Timestamp  int64             `json:"timestamp,omitempty"`
}

// Match is the result of a successful lookup. Multiple Matches may
// be returned for the same endpoint (the user may have placed a
// device in several dashboards/blocks).
type Match struct {
	Dashboard string `json:"dashboard"`
	Block     string `json:"block"`
	Item      Item   `json:"item"`
}

// Load reads, parses and validates the JSON file at path. A missing
// file returns (nil, nil) вЂ” a database-less environment is a valid
// state, not an error. A present-but-malformed file returns a
// non-nil error so the operator can be told that something is off.
func Load(path string) (*Database, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("homedweb: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var db Database
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("homedweb: parse %s: %w", path, err)
	}
	return &db, nil
}

// Provider is a thread-safe wrapper around a Database. It is
// intended to be constructed once at start-up and shared by every
// MCP tool that needs to look up user-defined names.
//
// A zero Provider is valid and behaves like "no database loaded".
type Provider struct {
	mu       sync.RWMutex
	db       *Database
	log      *logger.Logger
	aliasFor func(string) string
}

// NewProvider builds a Provider and (optionally) pre-loads it from
// path. A failure to load is non-fatal: the Provider is returned
// with a nil database and the error is reported to the caller.
//
// This is a thin wrapper around NewProviderWithLogger(path, nil) kept
// for backward compatibility with code that does not care about
// structured logging.
func NewProvider(path string) (*Provider, error) {
	return NewProviderWithLogger(path, nil)
}

// NewProviderWithLogger is like NewProvider but also wires a
// structured logger used to dump the contents of the database.json
// file at start-up (on debug) and to report load errors (on info).
// A nil logger is allowed; logging is then simply disabled.
func NewProviderWithLogger(path string, log *logger.Logger) (*Provider, error) {
	p := &Provider{log: log}
	if path == "" {
		return p, nil
	}
	if err := p.loadFromDisk(path); err != nil {
		return p, err
	}
	return p, nil
}

// loadFromDisk reads, parses and installs the database, logging the
// relevant details to the configured logger.
func (p *Provider) loadFromDisk(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if p.log != nil {
				p.log.Infof("homedweb: %s does not exist, continuing without user-defined names", path)
			}
			return nil
		}
		if p.log != nil {
			p.log.Infof("homedweb: read %s: %s", path, err)
		}
		return fmt.Errorf("homedweb: read %s: %w", path, err)
	}
	if len(raw) == 0 {
		if p.log != nil {
			p.log.Infof("homedweb: %s is empty, continuing without user-defined names", path)
		}
		return nil
	}
	if p.log != nil {
		if len(raw) > 4096 {
			p.log.Debugf("homedweb: read %s (%d bytes) contents=%s...", path, len(raw), truncateString(string(raw), 4096))
		} else {
			p.log.Debugf("homedweb: read %s contents=%s", path, string(raw))
		}
	}
	var db Database
	if err := json.Unmarshal(raw, &db); err != nil {
		if p.log != nil {
			p.log.Infof("homedweb: parse %s: %s", path, err)
		}
		return fmt.Errorf("homedweb: parse %s: %w", path, err)
	}
	p.mu.Lock()
	p.db = &db
	p.mu.Unlock()
	if p.log != nil {
		dashboards := len(db.Dashboards)
		names := len(db.Names)
		blocks := 0
		items := 0
		for _, d := range db.Dashboards {
			blocks += len(d.Blocks)
			for _, b := range d.Blocks {
				items += len(b.Items)
			}
		}
		p.log.Infof("homedweb: loaded %s (version=%s, dashboards=%d, blocks=%d, items=%d, names=%d)",
			path, db.Version, dashboards, blocks, items, names)
	}
	return nil
}

// Load replaces the currently stored database with a freshly parsed
// copy of the file at path. The previous value is discarded.
// Loading a missing file clears the database.
func (p *Provider) Load(path string) error {
	return p.loadFromDisk(path)
}

// HasDatabase reports whether a database has been loaded.
func (p *Provider) HasDatabase() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.db != nil
}

// Database returns a snapshot of the currently loaded database, or
// nil if no database has been loaded yet. The returned pointer is
// safe to read but must not be mutated by the caller.
func (p *Provider) Database() *Database {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.db
}

// Lookup returns every Match where the given endpoint appears in a
// dashboard block. If expose or property is non-empty, only items
// with a matching key are returned. An empty result is a valid
// outcome ("the user has not named this device/element").
//
// The endpoint in a dashboard item is always stored in its id form
// (e.g. "custom/alarm") because homed-web persists the id. But the
// caller may pass either form вЂ” id ("custom/alarm") or the broker-side
// name ("custom/РћС…СЂР°РЅР°" when the custom service runs with names=true).
// To make both forms work transparently we first try a strict match;
// if that fails and the endpoint has the shape "<service>/<id-or-name>",
// we additionally search by stripping the leading segment and looking
// up any item whose endpoint is the id-based counterpart discovered in
// the per-service "names" mapping exposed by the meta layer. Since the
// Provider does not have direct access to MQTT retain topics, we accept
// a pre-computed set of id<->name aliases via the optional
// SetEndpointAliases hook (see the companion Lookup call in the mcp
// package for the wiring).
func (p *Provider) Lookup(endpoint, expose, property string) []Match {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	if db == nil || endpoint == "" {
		return nil
	}
	var out []Match
	direct := lookupExact(db, endpoint, expose, property)
	out = append(out, direct...)
	if len(out) > 0 {
		return out
	}
	// Fallback: if aliases were registered, look up the canonical
	// (id-based) form of this endpoint and use the resulting matches.
	if p.aliasFor != nil {
		if canonical := p.aliasFor(endpoint); canonical != "" && canonical != endpoint {
			aliased := lookupExact(db, canonical, expose, property)
			out = append(out, aliased...)
		}
	}
	return out
}

// lookupExact performs the original strict equality search. Extracted
// so that Lookup can chain the exact and aliased lookups without
// duplicating the inner loops.
func lookupExact(db *Database, endpoint, expose, property string) []Match {
	var out []Match
	for _, dash := range db.Dashboards {
		for _, block := range dash.Blocks {
			for _, item := range block.Items {
				if item.Endpoint != endpoint {
					continue
				}
				if !matchKey(item, expose, property) {
					continue
				}
				out = append(out, Match{
					Dashboard: dash.Name,
					Block:     block.Name,
					Item:      item,
				})
			}
		}
	}
	return out
}

// SetEndpointAliases installs a function that maps an arbitrary
// endpoint (potentially in the broker-side name form) to the
// canonical id-based form stored in homed-web's database.json. When
// the map returns a non-empty value, Lookup tries the canonical form
// as a fallback after the direct lookup. Pass nil to clear the hook.
//
// The mcp package wires this up at start-up using the cached
// status/<service> payloads (see annotateNamesAware /
// resolveDeviceIdentifier).
func (p *Provider) SetEndpointAliases(aliasFor func(string) string) {
	p.mu.Lock()
	p.aliasFor = aliasFor
	p.mu.Unlock()
}

// LookupEndpoint returns every Match for the given endpoint, without
// filtering by expose/property. Useful for "list_devices" style
// tools that want to know every place a device is used.
func (p *Provider) LookupEndpoint(endpoint string) []Match {
	return p.Lookup(endpoint, "", "")
}

// LookupStatusName returns the user-friendly name for a
// "<endpoint>/<statusKey>" key, looked up in the top-level "names"
// dictionary. Returns "" if not present.
func (p *Provider) LookupStatusName(endpoint, statusKey string) string {
	p.mu.RLock()
	db := p.db
	p.mu.RUnlock()
	if db == nil || db.Names == nil {
		return ""
	}
	return db.Names[endpoint+"/"+statusKey]
}

// matchKey decides whether an Item should be returned for a given
// expose/property filter.
//
// Rules (mimicking the homed-web "expose OR property" semantics):
//   - If both filters are empty, every item matches.
//   - If only "expose" is set, items with that expose match (items
//     that use the "property" key are skipped).
//   - If only "property" is set, items with that property match.
//   - If both are set, either is accepted.
func matchKey(item Item, expose, property string) bool {
	if expose == "" && property == "" {
		return true
	}
	if expose != "" && item.Expose == expose {
		return true
	}
	if property != "" && item.Property == property {
		return true
	}
	return false
}

// truncateString returns the first n bytes of s, appending "..." when
// the string was actually clipped. It is used to keep single log
// lines from exploding when a homed-web database.json happens to be
// very large.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}