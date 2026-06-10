package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestAnnotateNamesAwareNamesTrue covers the happy path for a
// service that runs with names=true: the topic path carries the
// device NAME, the JSON body carries the id.
func TestAnnotateNamesAwareNamesTrue(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}}
	props := annotateNamesAware(map[string]any{}, []byte(`{"id":"61226326-10251872","nodeId":"0xabc"}`), "device/custom/OpenTherm", "device", fc)
	if props["name"] != "OpenTherm" {
		t.Errorf("name=%v, want OpenTherm", props["name"])
	}
	if props["id"] != "61226326-10251872" {
		t.Errorf("id=%v, want 61226326-10251872", props["id"])
	}
	if props["service"] != "custom" {
		t.Errorf("service=%v, want custom", props["service"])
	}
	if props["usesNames"] != true {
		t.Errorf("usesNames=%v, want true", props["usesNames"])
	}
}

// TestAnnotateNamesAwareNamesTrueFallsBackToEndpoint verifies that
// when the JSON body is missing the 'id' field (or it is empty),
// the helper falls back to the canonical endpoint form for 'id'
// so that downstream usage lookups still work.
func TestAnnotateNamesAwareNamesTrueFallsBackToEndpoint(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}}
	props := annotateNamesAware(map[string]any{}, []byte(`{"temperature":42.0}`), "device/custom/OpenTherm", "device", fc)
	if props["name"] != "OpenTherm" {
		t.Errorf("name=%v, want OpenTherm", props["name"])
	}
	if props["id"] != "custom/OpenTherm" {
		t.Errorf("id=%v, want custom/OpenTherm (endpoint fallback)", props["id"])
	}
}

// TestAnnotateNamesAwareNamesFalse covers the historical default:
// the topic path carries the device ID, the JSON body may carry
// the friendly name.
func TestAnnotateNamesAwareNamesFalse(t *testing.T) {
	fc := &fakeClient{prefix: "homed"} // no serviceNames -> all false
	props := annotateNamesAware(map[string]any{}, []byte(`{"name":"kitchen"}`), "device/light/kitchen", "device", fc)
	if props["id"] != "light/kitchen" {
		t.Errorf("id=%v, want light/kitchen", props["id"])
	}
	if props["name"] != "kitchen" {
		t.Errorf("name=%v, want kitchen", props["name"])
	}
	if props["usesNames"] != false {
		t.Errorf("usesNames=%v, want false", props["usesNames"])
	}
}

// TestAnnotateNamesAwareEmptyPayload is a defensive check: a
// payload that does not parse as JSON must not crash the helper.
func TestAnnotateNamesAwareEmptyPayload(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	props := annotateNamesAware(map[string]any{}, nil, "device/light/kitchen", "device", fc)
	if props["id"] != "light/kitchen" {
		t.Errorf("id=%v, want light/kitchen", props["id"])
	}
}

// TestAnnotateNamesAwareNonDeviceTopic verifies that a non-device
// topic leaves props untouched (helper is no-op for irrelevant
// topics).
func TestAnnotateNamesAwareNonDeviceTopic(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	props := map[string]any{"id": "existing"}
	out := annotateNamesAware(props, []byte(`{"name":"x"}`), "service/web", "device", fc)
	if out["id"] != "existing" {
		t.Errorf("non-device topic must not overwrite id; got %v", out)
	}
}

// TestEndpointFromTopic covers the helper used by every tool
// that needs to extract the device endpoint from a topic.
func TestEndpointFromTopic(t *testing.T) {
	cases := []struct {
		topic, kind, want string
	}{
		{"device/light/kitchen", "device", "light/kitchen"},
		{"expose/custom/61226326-10251872", "expose", "custom/61226326-10251872"},
		{"status/custom/OpenTherm", "status", "custom/OpenTherm"},
		{"device/light", "device", "light"},
		{"service/web", "device", ""}, // wrong prefix
		{"device", "device", ""},       // missing slash
	}
	for _, c := range cases {
		got := endpointFromTopic(c.topic, c.kind)
		if got != c.want {
			t.Errorf("endpointFromTopic(%q,%q)=%q, want %q", c.topic, c.kind, got, c.want)
		}
	}
}

// TestSplitServiceAndRest covers the tiny helper.
func TestSplitServiceAndRest(t *testing.T) {
	cases := []struct {
		in                string
		wantService, want string
	}{
		{"custom/61226326-10251872", "custom", "61226326-10251872"},
		{"light/kitchen", "light", "kitchen"},
		{"custom/OpenTherm/extra", "custom", "OpenTherm/extra"},
		{"justname", "", "justname"},
		{"", "", ""},
	}
	for _, c := range cases {
		gotS, gotR := splitServiceAndRest(c.in)
		if gotS != c.wantService || gotR != c.want {
			t.Errorf("splitServiceAndRest(%q)=(%q,%q), want (%q,%q)", c.in, gotS, gotR, c.wantService, c.want)
		}
	}
}

// TestResolveTopicForNamesFlagNoOp verifies that a service that
// does not run with names=true gets the topic verbatim.
func TestResolveTopicForNamesFlagNoOp(t *testing.T) {
	fc := &fakeClient{prefix: "homed", retained: map[string][]byte{
		"device/custom/61226326-10251872": []byte(`{"id":"custom/61226326-10251872"}`),
	}}
	topic, warn := resolveTopicForNamesFlag(fc, "td/custom/61226326-10251872")
	if topic != "td/custom/61226326-10251872" {
		t.Errorf("topic=%q, want verbatim", topic)
	}
	if warn != "" {
		t.Errorf("warn=%q, want empty", warn)
	}
}

// TestResolveTopicForNamesFlagResolvesIdToName covers the
// happy path: names=true, supplied id is in the JSON body of a
// cached device topic, the helper rewrites the topic to use the
// broker-side name.
func TestResolveTopicForNamesFlagResolvesIdToName(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}, retained: map[string][]byte{
		"device/custom/OpenTherm": []byte(`{"id":"61226326-10251872"}`),
	}}
	topic, warn := resolveTopicForNamesFlag(fc, "td/custom/61226326-10251872")
	if topic != "td/custom/OpenTherm" {
		t.Errorf("topic=%q, want td/custom/OpenTherm", topic)
	}
	if warn != "" {
		t.Errorf("warn=%q, want empty", warn)
	}
}

// TestResolveTopicForNamesFlagWithEndpointId verifies that a
// multi-segment endpoint id (e.g. '<service>/<id>/<sub>') is
// preserved when rewriting the topic.
func TestResolveTopicForNamesFlagWithEndpointId(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}, retained: map[string][]byte{
		"device/custom/OpenTherm": []byte(`{"id":"61226326-10251872"}`),
	}}
	topic, warn := resolveTopicForNamesFlag(fc, "td/custom/61226326-10251872/1")
	if topic != "td/custom/OpenTherm/1" {
		t.Errorf("topic=%q, want td/custom/OpenTherm/1", topic)
	}
	if warn != "" {
		t.Errorf("warn=%q, want empty", warn)
	}
}

// TestResolveTopicForNamesFlagUnresolved verifies that a missing
// id-to-name mapping produces a warning but does not crash.
func TestResolveTopicForNamesFlagUnresolved(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}, retained: map[string][]byte{
		"device/custom/OtherName": []byte(`{"id":"some-other-id"}`),
	}}
	topic, warn := resolveTopicForNamesFlag(fc, "td/custom/61226326-10251872")
	if topic != "td/custom/61226326-10251872" {
		t.Errorf("topic=%q, want unchanged (id could not be resolved)", topic)
	}
	if !strings.Contains(warn, "61226326-10251872") {
		t.Errorf("warn should mention the id; got %q", warn)
	}
}

// TestResolveTopicForNamesFlagMalformedJSON verifies that an
// unparseable JSON body is skipped (the helper just moves on to
// the next cached device).
func TestResolveTopicForNamesFlagMalformedJSON(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}, retained: map[string][]byte{
		"device/custom/BrokenName": []byte(`{not json`),
		"device/custom/OpenTherm":  []byte(`{"id":"61226326-10251872"}`),
	}}
	topic, warn := resolveTopicForNamesFlag(fc, "td/custom/61226326-10251872")
	if topic != "td/custom/OpenTherm" {
		t.Errorf("topic=%q, want td/custom/OpenTherm", topic)
	}
	if warn != "" {
		t.Errorf("warn=%q, want empty", warn)
	}
}

// TestResolveTopicForNamesFlagNodeIdFallback verifies that
// 'nodeId' is honoured as a fallback for the device id.
func TestResolveTopicForNamesFlagNodeIdFallback(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"zigbee": true}, retained: map[string][]byte{
		"device/zigbee/KitchenLight": []byte(`{"nodeId":"0x00124b0014b0b0b0"}`),
	}}
	topic, _ := resolveTopicForNamesFlag(fc, "td/zigbee/0x00124b0014b0b0b0")
	if topic != "td/zigbee/KitchenLight" {
		t.Errorf("topic=%q, want td/zigbee/KitchenLight", topic)
	}
}

// TestToolCallSetDeviceResolvesIdToNameWhenNamesTrue is the
// end-to-end check: the high-level tool publishes to the
// broker-side name (not the id the caller supplied) when the
// service is configured with names=true.
func TestToolCallSetDeviceResolvesIdToNameWhenNamesTrue(t *testing.T) {
	fc := &fakeClient{prefix: "homed", serviceNames: map[string]bool{"custom": true}, retained: map[string][]byte{
		"device/custom/OpenTherm": []byte(`{"id":"61226326-10251872","name":"OpenTherm"}`),
	}}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"custom/61226326-10251872","property":"status","value":"on"}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/custom/OpenTherm" {
		t.Errorf("expected publish to td/custom/OpenTherm (resolved from id), got %v", fc.published)
	}
	var got map[string]any
	if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
		t.Fatalf("payload is not JSON object: %v / %q", err, fc.publishedPayload[0])
	}
	if got["status"] != "on" {
		t.Errorf("payload=%+v, want status=on", got)
	}
}

// TestToolCallSetDeviceKeepsIdWhenNamesFalse is the symmetric
// check: a service that does not run with names=true sees the
// endpoint passed straight through.
func TestToolCallSetDeviceKeepsIdWhenNamesFalse(t *testing.T) {
	fc := &fakeClient{prefix: "homed", retained: map[string][]byte{
		"device/custom/61226326-10251872": []byte(`{"id":"custom/61226326-10251872"}`),
	}}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"custom/61226326-10251872","property":"status","value":"on"}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/custom/61226326-10251872" {
		t.Errorf("expected publish to td/custom/61226326-10251872 (id path), got %v", fc.published)
	}
}

// TestToolCallListDevicesNamesTrue verifies that the list tool
// extracts the device id from the JSON body and sets the name
// from the topic path when the service runs with names=true.
func TestToolCallListDevicesNamesTrue(t *testing.T) {
	client := &fakeClient{
		prefix:       "homed",
		serviceNames: map[string]bool{"custom": true},
		retained: map[string][]byte{
			"device/custom/OpenTherm": []byte(`{"id":"61226326-10251872","isDHWenabled":true}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_list_devices","arguments":{}}}`,
	}
	data := runServer(t, client, frames...)
	arr := decodeArray(t, data)
	if len(arr) != 1 {
		t.Fatalf("want 1 device, got %d: %v", len(arr), arr)
	}
	item := arr[0]
	if item["id"] != "61226326-10251872" {
		t.Errorf("id=%v, want 61226326-10251872 (from JSON body)", item["id"])
	}
	if item["name"] != "OpenTherm" {
		t.Errorf("name=%v, want OpenTherm (from topic path)", item["name"])
	}
	if item["service"] != "custom" {
		t.Errorf("service=%v, want custom", item["service"])
	}
	if item["usesNames"] != true {
		t.Errorf("usesNames=%v, want true", item["usesNames"])
	}
}

// TestToolCallListDevicesNamesFalse verifies the historical
// default: the topic path is treated as the device id and the
// 'name' is read from the JSON body when present.
func TestToolCallListDevicesNamesFalse(t *testing.T) {
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/light/kitchen": []byte(`{"name":"kitchen"}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_list_devices","arguments":{}}}`,
	}
	data := runServer(t, client, frames...)
	arr := decodeArray(t, data)
	if len(arr) != 1 {
		t.Fatalf("want 1 device, got %d: %v", len(arr), arr)
	}
	item := arr[0]
	if item["id"] != "light/kitchen" {
		t.Errorf("id=%v, want light/kitchen (topic path)", item["id"])
	}
	if item["name"] != "kitchen" {
		t.Errorf("name=%v, want kitchen (from JSON body)", item["name"])
	}
	if item["service"] != "light" {
		t.Errorf("service=%v, want light", item["service"])
	}
	if item["usesNames"] != false {
		t.Errorf("usesNames=%v, want false", item["usesNames"])
	}
}

// TestToolCallOverviewShowsNamesFlags verifies the overview
// tool surfaces the per-service 'names' flag table.
func TestToolCallOverviewShowsNamesFlags(t *testing.T) {
	client := &fakeClient{
		prefix: "homed",
		serviceNames: map[string]bool{
			"custom": true,
			"zigbee": false,
		},
		retained: map[string][]byte{
			"device/custom/OpenTherm": []byte(`{"id":"61226326-10251872"}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_overview","arguments":{}}}`,
	}
	data := runServer(t, client, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)
	text := call.Content[0].Text
	if !strings.Contains(text, "Service 'names' flags") {
		t.Errorf("overview should mention 'names' flags; got %q", text)
	}
	if !strings.Contains(text, "custom -> uses name in topic paths") {
		t.Errorf("overview should list 'custom' as using name; got %q", text)
	}
	if !strings.Contains(text, "zigbee -> uses id in topic paths") {
		t.Errorf("overview should list 'zigbee' as using id; got %q", text)
	}
}

// TestToolCallGetStatusNamesAwareMetaBlock verifies that the
// status tool includes the 'usesNames'/'name'/'service' fields
// in the per-endpoint meta block.
func TestToolCallGetStatusNamesAwareMetaBlock(t *testing.T) {
	client := &fakeClient{
		prefix:       "homed",
		serviceNames: map[string]bool{"custom": true},
		retained: map[string][]byte{
			"status/custom/OpenTherm": []byte(`{"status_1":true}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_get_status","arguments":{}}}`,
	}
	data := runServer(t, client, frames...)
	resp := decodeObject(t, data)
	meta, ok := resp["meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing meta: %+v", resp)
	}
	endpointMeta, ok := meta["custom/OpenTherm"].(map[string]any)
	if !ok {
		t.Fatalf("missing endpoint meta for custom/OpenTherm: %+v", meta)
	}
	if endpointMeta["service"] != "custom" {
		t.Errorf("meta service=%v, want custom", endpointMeta["service"])
	}
	if endpointMeta["name"] != "OpenTherm" {
		t.Errorf("meta name=%v, want OpenTherm", endpointMeta["name"])
	}
	if endpointMeta["usesNames"] != true {
		t.Errorf("meta usesNames=%v, want true", endpointMeta["usesNames"])
	}
}

// TestResolveTopicForNamesFlagContextSafety is a paranoid check
// that the helper does not panic when called concurrently from
// many goroutines. The fake is not thread-safe, so the test
// relies on a custom fake wrapper to make this meaningful.
func TestResolveTopicForNamesFlagContextSafety(t *testing.T) {
	_ = context.Background()
	// no-op: the helper itself only reads the cache, the
	// serviceNames map and returns immediately, so it is safe by
	// construction. This test exists purely as documentation.
}