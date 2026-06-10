// Tests for the endpoint-alias resolver and the small helpers that
// feed it. These tests intentionally use a fake aliasClient
// implementation rather than spinning up an MQTT broker: the
// resolver is supposed to be a pure function of the retained
// payload cache, and verifying it as such is the whole point of
// extracting the computation in computeAliasMap().
package main

import (
	"sync"
	"testing"
)

// fakeAliasClient is an aliasClient-compatible test double. It
// records every callback registered via OnRetained and lets the
// test fire them on demand to simulate broker activity.
type fakeAliasClient struct {
	mu        sync.Mutex
	retained  map[string][]byte
	callbacks []func(topic string, payload []byte)
}

func newFakeAliasClient() *fakeAliasClient {
	return &fakeAliasClient{retained: map[string][]byte{}}
}

func (f *fakeAliasClient) Retained() map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string][]byte, len(f.retained))
	for k, v := range f.retained {
		out[k] = v
	}
	return out
}

func (f *fakeAliasClient) OnRetained(cb func(topic string, payload []byte)) {
	if cb == nil {
		return
	}
	f.mu.Lock()
	f.callbacks = append(f.callbacks, cb)
	f.mu.Unlock()
}

// put mirrors the broker: it updates the retained cache and then
// fires every registered retained-hook so the resolver sees the
// same sequence of events it would see against a real MQTT
// connection. Tests that want to inject state *before* the
// resolver exists can use putBeforeSubscribe() to skip the
// callback fan-out.
func (f *fakeAliasClient) put(topic string, payload []byte) {
	f.mu.Lock()
	f.retained[topic] = payload
	cbs := append([]func(string, []byte){}, f.callbacks...)
	f.mu.Unlock()
	for _, cb := range cbs {
		cb(topic, payload)
	}
}

// putBeforeSubscribe is the cold-start variant: it populates the
// retained cache but does NOT fire hooks. Use it for the rare
// case where the test wants to construct the resolver with the
// state already in place (the resolver's prime-time scan will
// then see the data). The standard put() helper above is
// preferable in every other case.
func (f *fakeAliasClient) putBeforeSubscribe(topic string, payload []byte) {
	f.mu.Lock()
	f.retained[topic] = payload
	f.mu.Unlock()
}

func (f *fakeAliasClient) fire(topic string, payload []byte) {
	f.mu.Lock()
	cbs := append([]func(string, []byte){}, f.callbacks...)
	f.mu.Unlock()
	for _, cb := range cbs {
		cb(topic, payload)
	}
}

// TestResolver_StatusService_CanonicalSource reproduces the
// 2026-06-08 production bug: five BLE thermometers that publish
// byte-identical "expose/<service>/<X>" payloads, but distinct
// "status/<service>" entries. The resolver MUST return five
// independent id<->name pairs and MUST NOT collapse them into a
// single alias via payload fingerprinting.
func TestResolver_StatusService_CanonicalSource(t *testing.T) {
	const service = "custom"
	ble := []struct{ id, name string }{
		{"a4:c1:38:01:00:01", "bleHall"},
		{"a4:c1:38:01:00:02", "bleHallOld"},
		{"a4:c1:38:01:00:03", "bleShower"},
		{"a4:c1:38:01:00:04", "ble_bedroom"},
		{"a4:c1:38:01:00:05", "bleBedroomM"},
	}
	// All five devices publish the same "expose" payload (this
	// is exactly the scenario that broke the previous
	// fingerprint-based grouping). The canonical source is the
	// "status/<service>" payload, which lists every device with
	// its own id+name.
	expose := []byte(`{"items":["battery","rssi","last","humidity","temperature"],"options":{"temperature":{"class":"temperature"}}}`)
	status := []byte(`{"names":true,"devices":[` +
		`{"id":"a4:c1:38:01:00:01","name":"bleHall"},` +
		`{"id":"a4:c1:38:01:00:02","name":"bleHallOld"},` +
		`{"id":"a4:c1:38:01:00:03","name":"bleShower"},` +
		`{"id":"a4:c1:38:01:00:04","name":"ble_bedroom"},` +
		`{"id":"a4:c1:38:01:00:05","name":"bleBedroomM"}` +
		`]}`)

	c := newFakeAliasClient()
	// The old, broken behaviour would have merged all five
	// endpoints via the shared "expose/<service>/<X>" payload.
	// We deliberately keep those topics populated to make sure
	// the new resolver ignores them.
	for _, d := range ble {
		c.put("expose/"+service+"/"+d.id, expose)
		c.put("expose/"+service+"/"+d.name, expose)
	}
	c.put("status/"+service, status)

	resolver := buildEndpointAliasResolver(c, nil)

	for _, d := range ble {
		gotID := resolver(service + "/" + d.name)
		if gotID != service+"/"+d.id {
			t.Errorf("resolver(%q) = %q, want %q (BLE aliasing by expose fingerprint leaked through)",
				service+"/"+d.name, gotID, service+"/"+d.id)
		}
		gotName := resolver(service + "/" + d.id)
		if gotName != service+"/"+d.name {
			t.Errorf("resolver(%q) = %q, want %q (reverse alias missing)",
				service+"/"+d.id, gotName, service+"/"+d.name)
		}
	}

	// Sanity-check the bidirectional count: every entry has its
	// own pair, so the canonical map is exactly 5*2 = 10 entries.
	snapshot := computeAliasMap(c, nil, 5)
	if got, want := len(snapshot), 2*len(ble); got != want {
		t.Errorf("snapshot size = %d, want %d (5 BLE devices must remain independent)", got, want)
	}
}

// TestResolver_StatusService_InvalidatesOnRetained ensures the
// snapshot is refreshed when a new "status/<service>" retain
// arrives after the resolver has already served lookups.
func TestResolver_StatusService_InvalidatesOnRetained(t *testing.T) {
	const service = "custom"
	c := newFakeAliasClient()
	c.put("status/"+service, []byte(`{"names":true,"devices":[{"id":"dev1","name":"first"}]}`))

	resolver := buildEndpointAliasResolver(c, nil)
	if got := resolver(service + "/first"); got != service+"/dev1" {
		t.Fatalf("resolver before refresh: got %q, want %q", got, service+"/dev1")
	}

	// Broker receives a new retain: the service is reconfigured
	// and a new device is added.
	c.put("status/"+service, []byte(`{"names":true,"devices":[`+
		`{"id":"dev1","name":"first"},`+
		`{"id":"dev2","name":"second"}`+
		`]}`))
	c.fire("status/"+service, []byte(`{"names":true,"devices":[`+
		`{"id":"dev1","name":"first"},`+
		`{"id":"dev2","name":"second"}`+
		`]}`))

	// The next lookup MUST see "second" -> "dev2". If the
	// resolver served the stale snapshot, it would return "".
	if got := resolver(service + "/second"); got != service+"/dev2" {
		t.Errorf("resolver after refresh: got %q, want %q", got, service+"/dev2")
	}
	if got := resolver(service + "/first"); got != service+"/dev1" {
		t.Errorf("resolver after refresh: first alias broken, got %q, want %q", got, service+"/dev1")
	}
}

// TestResolver_ColdStart_DeviceRetain covers the case where no
// "status/<service>" payload is available yet (the service has
// not had a chance to publish it) and the resolver has to fall
// back on "device/<service>/<X>" retains grouped by
// availabilityTopic + lastSeen.
func TestResolver_ColdStart_DeviceRetain(t *testing.T) {
	c := newFakeAliasClient()
	now := int64(1749400000)
	c.put("device/custom/alarm", []byte(`{"lastSeen":`+itoa(now)+`,"availabilityTopic":"availability/custom/alarm","status":"online"}`))
	c.put("device/custom/РћС…СЂР°РЅР°", []byte(`{"lastSeen":`+itoa(now+1)+`,"availabilityTopic":"availability/custom/alarm","status":"online"}`))
	// Different availabilityTopic, must not be merged.
	c.put("device/custom/other", []byte(`{"lastSeen":`+itoa(now)+`,"availabilityTopic":"availability/custom/other","status":"online"}`))

	resolver := buildEndpointAliasResolver(c, nil)
	if got := resolver("custom/РћС…СЂР°РЅР°"); got != "custom/alarm" {
		t.Errorf("cold-start: resolver(РћС…СЂР°РЅР°) = %q, want %q", got, "custom/alarm")
	}
	if got := resolver("custom/alarm"); got != "custom/РћС…СЂР°РЅР°" {
		t.Errorf("cold-start: reverse alias missing, got %q, want %q", got, "custom/РћС…СЂР°РЅР°")
	}
	// "custom/other" has a unique availabilityTopic; it must
	// not be silently aliased to anything.
	if got := resolver("custom/other"); got != "" {
		t.Errorf("cold-start: unexpected alias for singleton, got %q, want \"\"", got)
	}
}

// TestResolver_NoAliasesForUnknown verifies the resolver does not
// invent aliases for endpoints it has never seen, including the
// case where the retained cache is completely empty.
func TestResolver_NoAliasesForUnknown(t *testing.T) {
	c := newFakeAliasClient()
	resolver := buildEndpointAliasResolver(c, nil)
	if got := resolver("custom/anything"); got != "" {
		t.Errorf("empty cache: got %q, want \"\"", got)
	}
	c.put("status/custom", []byte(`{"names":true,"devices":[{"id":"X","name":"Y"}]}`))
	c.fire("status/custom", []byte(`{"names":true,"devices":[{"id":"X","name":"Y"}]}`))
	if got := resolver("custom/missing"); got != "" {
		t.Errorf("known service, unknown device: got %q, want \"\"", got)
	}
}

// TestResolver_IgnoresUnrelatedRetained ensures the snapshot is
// NOT invalidated by a retain on a topic that cannot affect the
// alias map (e.g. a transient state tick on status/<device>).
func TestResolver_IgnoresUnrelatedRetained(t *testing.T) {
	c := newFakeAliasClient()
	c.put("status/custom", []byte(`{"names":true,"devices":[{"id":"X","name":"Y"}]}`))
	c.fire("status/custom", []byte(`{"names":true,"devices":[{"id":"X","name":"Y"}]}`))
	resolver := buildEndpointAliasResolver(c, nil)
	if got := resolver("custom/Y"); got != "custom/X" {
		t.Fatalf("priming failed: got %q, want %q", got, "custom/X")
	}
	// Force the snapshot to be "clean" by issuing a lookup, then
	// fire a retain on an unrelated topic. The map MUST be
	// served from the snapshot.
	c.fire("status/custom/0x1234", []byte(`{"status":"on"}`))
	if got := resolver("custom/Y"); got != "custom/X" {
		t.Errorf("snapshot corrupted by unrelated retain: got %q, want %q", got, "custom/X")
	}
}

// TestAddAlias_Idempotent protects addAlias from regressing into
// a flooder: a second call with a different partner must not
// silently rewrite the first mapping.
func TestAddAlias_Idempotent(t *testing.T) {
	out := map[string]string{}
	addAlias(out, "a", "b", nil)
	addAlias(out, "a", "c", nil) // ignored
	addAlias(out, "b", "d", nil) // ignored
	if out["a"] != "b" {
		t.Errorf("a mapped to %q, want %q", out["a"], "b")
	}
	if out["b"] != "a" {
		t.Errorf("b mapped to %q, want %q", out["b"], "a")
	}
	if _, present := out["c"]; present {
		t.Errorf("c must not have been inserted: %q", out["c"])
	}
	if _, present := out["d"]; present {
		t.Errorf("d must not have been inserted: %q", out["d"])
	}
}

// itoa is a tiny helper so the tests do not have to import
// strconv just to build fake payloads.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}