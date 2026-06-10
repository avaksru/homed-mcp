package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/u236/homed-mcp/internal/homedweb"
)

// fakeClient implements MQTTClient for tests.
type fakeClient struct {
	mu               sync.Mutex
	prefix           string
	retained         map[string][]byte
	live             map[string][]byte
	published        []string
	publishedPayload [][]byte
	publishedRetain  []bool
	// waitTopic and waitPayload, if set, cause WaitFor to return them
	// successfully (used by homed_get_properties tests).
	waitTopic   string
	waitPayload []byte
	// serviceNames mimics the per-service 'names' retain flag
	// published by the HOMEd service on {prefix}/status/<service>.
	// Tests that exercise the names-related logic set this map
	// directly.
	serviceNames map[string]bool
}

func (f *fakeClient) Prefix() string              { return f.prefix }
func (f *fakeClient) Topic(s string) string       { return f.prefix + "/" + s }
func (f *fakeClient) Retained() map[string][]byte { return f.retained }
func (f *fakeClient) Live() map[string][]byte {
	if f.live == nil {
		return map[string][]byte{}
	}
	return f.live
}
func (f *fakeClient) Subscribe(string, byte) error { return nil }
func (f *fakeClient) Unsubscribe(string) error     { return nil }
func (f *fakeClient) Publish(s string, payload any, retain bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, s)
	if payload == nil {
		f.publishedPayload = append(f.publishedPayload, nil)
	} else if b, err := json.Marshal(payload); err == nil {
		f.publishedPayload = append(f.publishedPayload, b)
	} else {
		f.publishedPayload = append(f.publishedPayload, nil)
	}
	f.publishedRetain = append(f.publishedRetain, retain)
	return nil
}
func (f *fakeClient) Request(_ context.Context, _ string, _ map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}
func (f *fakeClient) WaitFor(_ context.Context, _ string, _ time.Duration) (string, []byte, error) {
	if f.waitTopic != "" {
		return f.waitTopic, f.waitPayload, nil
	}
	return "", nil, fmt.Errorf("not implemented in fake")
}
func (f *fakeClient) ServiceUsesNames(service string) bool {
	if service == "" {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.serviceNames == nil {
		return false
	}
	return f.serviceNames[service]
}
func (f *fakeClient) ServiceNames() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool, len(f.serviceNames))
	for k, v := range f.serviceNames {
		out[k] = v
	}
	return out
}

// fakeMeta implements MetaSource for tests. When populated from a
// JSON file, the underlying homedweb.Provider handles Lookup.
// Otherwise it behaves like the noopMeta.
type fakeMeta struct {
	*homedweb.Provider
}

// newMetaFromFile builds a MetaSource backed by the sample database.json
// in the test directory. It returns noopMeta-equivalent when the file
// is missing.
func newMetaFromFile(t *testing.T, path string) MetaSource {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	p, err := homedweb.NewProvider(path)
	if err != nil {
		t.Fatalf("meta from %s: %v", path, err)
	}
	return fakeMeta{Provider: p}
}

// runServer executes the server with the given stdin frames and returns the
// raw stdout bytes (newline delimited JSON-RPC responses).
func runServer(t *testing.T, client MQTTClient, frames ...string) []byte {
	t.Helper()

	srv := NewServer("test", "0.0.0")
	// Tests use the noop meta provider so that no JSON file is
	// needed. Tool-level tests that need user-defined names call
	// runServerWithMeta directly.
	RegisterHOMEdTools(srv, client, nil)

	in := strings.NewReader(strings.Join(frames, "\n") + "\n")
	out := &bytes.Buffer{}
	if err := srv.Run(context.Background(), in, out); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.Bytes()
}

// parseResponses splits the output into individual JSON-RPC envelopes.
func parseResponses(t *testing.T, data []byte) []*Response {
	t.Helper()
	var out []*Response
	for _, line := range bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var r Response
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		out = append(out, &r)
	}
	return out
}

func TestInitialize(t *testing.T) {
	client := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	data := runServer(t, client, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d: %s", len(resps), data)
	}

	var init InitializeResult
	mustMarshal(t, resps[0].Result, &init)
	if init.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion=%q", init.ProtocolVersion)
	}
	if init.ServerInfo.Name != "test" {
		t.Errorf("server name=%q", init.ServerInfo.Name)
	}
}

func TestToolsList(t *testing.T) {
	client := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}
	data := runServer(t, client, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}

	var list ListToolsResult
	mustMarshal(t, resps[1].Result, &list)

	got := map[string]bool{}
	for _, td := range list.Tools {
		got[td.Name] = true
	}
	want := []string{
		"homed_list_devices",
		"homed_list_services",
		"homed_list_exposes",
		"homed_overview",
		"homed_get_status",
		"homed_get_topic",
		"homed_get_request",
		"homed_publish",
		"homed_set_device",
		"homed_subscribe",
		"homed_unsubscribe",
		"homed_get_properties",
		"homed_list_live",
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("tool %q not registered (got: %v)", n, list.Tools)
		}
	}
}

func TestToolCallListDevices(t *testing.T) {
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/light/kitchen": []byte(`{"name":"kitchen"}`),
			"device/switch/hall":   []byte(`{"name":"hall"}`),
			"service/web":          []byte(`{"version":"1"}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_list_devices","arguments":{}}}`,
	}
	data := runServer(t, client, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}

	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)

	// The result text is a JSON array of objects with "id" field.
	if len(call.Content) != 1 || call.Content[0].Type != "text" {
		t.Fatalf("bad content: %+v", call)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(call.Content[0].Text), &arr); err != nil {
		t.Fatalf("text is not JSON array: %v / %q", err, call.Content[0].Text)
	}
	if len(arr) != 2 {
		t.Fatalf("want 2 devices, got %d: %v", len(arr), arr)
	}
	ids := map[string]bool{}
	for _, item := range arr {
		ids[fmtSprint(item["id"])] = true
	}
	if !ids["light/kitchen"] || !ids["switch/hall"] {
		t.Errorf("missing expected device ids: %v", arr)
	}
}

// TestToolCallListExposesWithMeta verifies that homed_list_exposes
// enriches its output with the "usage" array when a MetaSource is
// wired in.
func TestToolCallListExposesWithMeta(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.json")
	if err := os.WriteFile(dbPath, []byte(sampleMetaDB), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"expose/custom/61226326-10251872": []byte(`{"id":"custom/61226326-10251872","isDHWenabled":true}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"homed_list_exposes","arguments":{}}}`,
	}
	meta := newMetaFromFile(t, dbPath)
	data := runServerWithMeta(t, client, meta, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)
	if len(call.Content) != 1 || call.Content[0].Type != "text" {
		t.Fatalf("bad content: %+v", call)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(call.Content[0].Text), &arr); err != nil {
		t.Fatalf("text is not JSON: %v / %q", err, call.Content[0].Text)
	}
	if len(arr) != 1 {
		t.Fatalf("want 1 expose, got %d: %v", len(arr), arr)
	}
	usage, ok := arr[0]["usage"].([]any)
	if !ok || len(usage) == 0 {
		t.Fatalf("missing usage array: %+v", arr[0])
	}
	first := usage[0].(map[string]any)
	if first["dashboard"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎвЂќР В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІР‚С”Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р РЋРІР‚Сљ" || first["block"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎСљР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎС›Р В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІвЂћСћР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»" {
		t.Errorf("unexpected usage entry: %+v", first)
	}
}

// sampleMetaDB is a compact database.json fragment that exercises
// every Lookup path used by the tools:
//
//   - expose-style items (custom/<uuid> + expose name)
//   - status_<N> renames via the top-level "names" dictionary
//   - a property-style item to cover homed_get_properties
const sampleMetaDB = `{
  "names": {
    "custom/14705744-45074752/status_2": "Р В Р Р‹Р В РІР‚С™Р В Р Р‹Р РЋРЎСџР В Р Р‹Р Р†РІР‚С›РЎС›Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В Р’В Р В РІР‚в„–",
    "custom/14705744-45074752/status_15": "Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎС™Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’Вµ"
  },
  "dashboards": [
    {
      "name": "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎвЂќР В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІР‚С”Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р РЋРІР‚Сљ",
      "blocks": [
        {
          "name": "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎСљР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎС›Р В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІвЂћСћР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»",
          "items": [
            {"endpoint": "custom/61226326-10251872", "expose": "isDHWenabled", "name": "Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В Р’В Р В РІР‚в„–"},
            {"endpoint": "custom/61226326-10251872", "expose": "OTget25",      "name": "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р РЋРЎСџР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎС›Р В Р’В Р вЂ™Р’В Р В РЎС›Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р В Р вЂ№Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°"},
            {"endpoint": "custom/14705744-45074752", "property": "pressure",   "name": "Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎС™Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’Вµ"}
          ]
        }
      ]
    }
  ]
}`

// TestToolCallListDevicesWithMeta verifies that homed_list_devices
// attaches a usage array sourced from the homed-web database.
func TestToolCallListDevicesWithMeta(t *testing.T) {
	dbPath := writeMetaDB(t)
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/custom/61226326-10251872": []byte(`{"id":"custom/61226326-10251872","name":"otter"}`),
		},
	}
	data := runWithMeta(t, client, dbPath, callFrames("homed_list_devices"))
	arr := decodeArray(t, data)
	if len(arr) != 1 {
		t.Fatalf("want 1 device, got %d: %v", len(arr), arr)
	}
	usage, ok := arr[0]["usage"].([]any)
	if !ok || len(usage) == 0 {
		t.Fatalf("missing usage: %+v", arr[0])
	}
}

// TestToolCallGetStatusWithMeta verifies that homed_get_status
// attaches a per-endpoint meta block combining usage and key-level
// renames.
func TestToolCallGetStatusWithMeta(t *testing.T) {
	dbPath := writeMetaDB(t)
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"status/custom/14705744-45074752": []byte(`{"status_2":true,"status_15":1.2,"status_99":"x"}`),
		},
	}
	data := runWithMeta(t, client, dbPath, callFrames("homed_get_status", `{"topic":"custom/14705744-45074752"}`))
	resp := decodeObject(t, data)
	meta, ok := resp["meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing meta: %+v", resp)
	}
	endpointMeta, ok := meta["custom/14705744-45074752"].(map[string]any)
	if !ok {
		t.Fatalf("missing endpoint meta: %+v", meta)
	}
	if names, _ := endpointMeta["status_2"].(string); names != "Р В Р Р‹Р В РІР‚С™Р В Р Р‹Р РЋРЎСџР В Р Р‹Р Р†РІР‚С›РЎС›Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В Р’В Р В РІР‚в„–" {
		t.Errorf("status_2 rename=%q", names)
	}
	if names, _ := endpointMeta["status_15"].(string); names != "Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎС™Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’Вµ" {
		t.Errorf("status_15 rename=%q", names)
	}
	if _, present := endpointMeta["status_99"]; present {
		t.Errorf("unexpected rename for status_99: %+v", endpointMeta)
	}
	if _, ok := endpointMeta["__usage__"]; !ok {
		t.Errorf("missing __usage__ for status endpoint: %+v", endpointMeta)
	}
}

// TestToolCallGetTopicWithMeta verifies that homed_get_topic
// attaches a meta block with statusKey-level renames.
func TestToolCallGetTopicWithMeta(t *testing.T) {
	dbPath := writeMetaDB(t)
	client := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"status/custom/14705744-45074752": []byte(`{"status_2":true,"status_15":1.2}`),
		},
	}
	data := runWithMeta(t, client, dbPath, callFrames("homed_get_topic", `{"topic":"status/custom/14705744-45074752"}`))
	resp := decodeObject(t, data)
	meta, ok := resp["meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing meta: %+v", resp)
	}
	names, _ := meta["names"].(map[string]any)
	if names["status_2"] != "Р В Р Р‹Р В РІР‚С™Р В Р Р‹Р РЋРЎСџР В Р Р‹Р Р†РІР‚С›РЎС›Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В Р’В Р В РІР‚в„–" || names["status_15"] != "Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎС™Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’Вµ" {
		t.Errorf("unexpected names: %+v", names)
	}
}

// TestToolCallGetRequestWithMeta verifies that homed_get_request
// attaches a meta.usage block when the request topic matches an
// endpoint defined in homed-web.
func TestToolCallGetRequestWithMeta(t *testing.T) {
	dbPath := writeMetaDB(t)
	client := &fakeClient{prefix: "homed"}
	data := runWithMeta(t, client, dbPath, callFrames("homed_get_request", `{"topic":"custom/61226326-10251872","message":{"action":"get"}}`))
	resp := decodeObject(t, data)
	meta, ok := resp["meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing meta: %+v", resp)
	}
	usage, ok := meta["usage"].([]any)
	if !ok || len(usage) == 0 {
		t.Fatalf("missing usage: %+v", meta)
	}
	first := usage[0].(map[string]any)
	if first["dashboard"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎвЂќР В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІР‚С”Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р РЋРІР‚Сљ" || first["block"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎСљР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎС›Р В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІвЂћСћР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»" {
		t.Errorf("unexpected usage: %+v", first)
	}
}

// TestToolCallGetPropertiesWithMeta verifies that homed_get_properties
// attaches a usage array based on the service/device pair.
func TestToolCallGetPropertiesWithMeta(t *testing.T) {
	dbPath := writeMetaDB(t)
	client := &fakeClient{
		prefix:      "homed",
		waitTopic:   "fd/custom/14705744-45074752",
		waitPayload: []byte(`{"pressure":1.2}`),
	}
	data := runWithMeta(t, client, dbPath, callFrames("homed_get_properties", `{"service":"custom","device":"14705744-45074752"}`))
	resp := decodeObject(t, data)
	usage, ok := resp["usage"].([]any)
	if !ok || len(usage) == 0 {
		t.Fatalf("missing usage: %+v", resp)
	}
	first := usage[0].(map[string]any)
	if first["dashboard"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎвЂќР В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІР‚С”Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р РЋРІР‚Сљ" || first["block"] != "Р В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎСљР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљРЎС›Р В Р’В Р В Р вЂ№Р В Р вЂ Р В РІР‚С™Р РЋРІвЂћСћР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»" {
		t.Errorf("unexpected usage: %+v", first)
	}
}

// writeMetaDB writes sampleMetaDB to a temp file and returns the path.
func writeMetaDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "db.json")
	if err := os.WriteFile(path, []byte(sampleMetaDB), 0o600); err != nil {
		t.Fatalf("write db: %v", err)
	}
	return path
}

// callFrames produces the standard initialize + tools/call frames for
// the given tool name and (optional) arguments JSON. The trailing
// optional argument is the JSON body of "arguments": pass "" to mean
// {} (no arguments).
func callFrames(tool string, args ...string) []string {
	body := `{}`
	if len(args) > 0 && args[0] != "" {
		body = args[0]
	}
	return []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + body + `}}`,
	}
}

// runWithMeta is a thin convenience wrapper around runServerWithMeta
// that builds the MetaSource from a database.json file.
func runWithMeta(t *testing.T, client MQTTClient, dbPath string, frames []string) []byte {
	t.Helper()
	return runServerWithMeta(t, client, newMetaFromFile(t, dbPath), frames...)
}

// decodeArray decodes the second JSON-RPC response into a list of
// JSON objects.
func decodeArray(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d: %s", len(resps), data)
	}
	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)
	if len(call.Content) != 1 || call.Content[0].Type != "text" {
		t.Fatalf("bad content: %+v", call)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(call.Content[0].Text), &arr); err != nil {
		t.Fatalf("text is not JSON array: %v / %q", err, call.Content[0].Text)
	}
	return arr
}

// decodeObject decodes the second JSON-RPC response into a single
// JSON object (used by tools/call results that return objects).
func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d: %s", len(resps), data)
	}
	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)
	if len(call.Content) != 1 || call.Content[0].Type != "text" {
		t.Fatalf("bad content: %+v", call)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(call.Content[0].Text), &out); err != nil {
		t.Fatalf("text is not JSON object: %v / %q", err, call.Content[0].Text)
	}
	return out
}

// TestToDeviceTopic covers the topic rewrite rules used by
// homed_publish / homed_set_device. This is a pure function test Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™
// it does not need the JSON-RPC scaffolding.
func TestToDeviceTopic(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"device/light/kitchen", "td/light/kitchen"},
		{"device/custom/alarm", "td/custom/alarm"},
		{"custom/alarm", "td/custom/alarm"},
		{"td/custom/alarm", "td/custom/alarm"},
		{"td/custom/alarm/1", "td/custom/alarm/1"},
		{"command/custom", "command/custom"},
		{"command/custom/abc", "command/custom/abc"},
		{"status/custom/abc", "status/custom/abc"},
		{"expose/custom/abc", "expose/custom/abc"},
		{"service/web", "service/web"},
		{"fd/custom/abc", "fd/custom/abc"},
		{"response/whatever", "response/whatever"},
		{"single", "single"}, // no slashes Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ left alone
		{"a/b/c/d", "a/b/c/d"}, // too many slashes Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ left alone
	}
	for _, c := range cases {
		got := toDeviceTopic(c.in)
		if got != c.want {
			t.Errorf("toDeviceTopic(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestToolCallPublish verifies that homed_publish rewrites the
// legacy "device/<service>/<id>" topic into the HOMEd "td/" topic
// and forces retain=false for the rewritten control command. This
// is the format homed-web uses for switch exposes (and what the
// rest of the HOMEd ecosystem expects for device control).
func TestToolCallPublish(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_publish","arguments":{"topic":"device/light/kitchen","message":{"action":"set","value":true}}}}`,
	}
	data := runServer(t, fc, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	if len(fc.published) != 1 || fc.published[0] != "td/light/kitchen" {
		t.Errorf("expected publish to td/light/kitchen, got %v", fc.published)
	}
	if len(fc.publishedRetain) != 1 || fc.publishedRetain[0] != false {
		t.Errorf("expected retain=false for td/ topic, got %v", fc.publishedRetain)
	}
	// Payload should be passed through unchanged (modulo JSON key
	// ordering). We don't care about the exact bytes here Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ only
	// that the value was delivered to the underlying client.
	if len(fc.publishedPayload) != 1 || len(fc.publishedPayload[0]) == 0 {
		t.Errorf("expected a non-empty payload, got %v", fc.publishedPayload)
	}
}

// TestIsAllowedPublishTopic covers the MCP-server publish
// allowlist. The check is intentionally simple: the topic must
// start with 'homed/command/' or 'homed/td/'. Anything else
// (status/, expose/, fd/, service/, response/, device/, wrong
// broker prefix, empty) must be rejected.
func TestIsAllowedPublishTopic(t *testing.T) {
	allowed := []string{
		"homed/command/custom",
		"homed/command/zigbee",
		"homed/command/custom/instance1",
		"homed/td/custom/26067540-57076820",
		"homed/td/custom/26067540-57076820/1",
		"homed/td/custom/instance1/26067540-57076820",
		"homed/td/custom/instance/26067540-57076820/endpoint",
		"homed/td/custom/26067540-57076820/extra/segment", // any depth under td/ is allowed
		"homed/td/zigbee/0x00124b0014b0b0b0/1",
	}
	for _, topic := range allowed {
		t.Run("allow/"+topic, func(t *testing.T) {
			if !isAllowedPublishTopic(topic) {
				t.Errorf("isAllowedPublishTopic(%q) = false, want true", topic)
			}
		})
	}
	denied := []string{
		"",
		"homed",
		"homed/",
		"homed/status/custom",
		"homed/status/custom/garland_window",
		"homed/expose/custom/garland_window",
		"homed/device/custom/garland_window",
		"homed/service/custom",
		"homed/fd/custom/garland_window",
		"homed/response/whatever",
		"other/command/custom",                  // wrong broker prefix
		"other/td/custom/garland_window",        // wrong broker prefix
		"command/custom",                        // broker prefix missing
		"td/custom/garland_window",              // broker prefix missing
	}
	for _, topic := range denied {
		t.Run("deny/"+topic, func(t *testing.T) {
			if isAllowedPublishTopic(topic) {
				t.Errorf("isAllowedPublishTopic(%q) = true, want false", topic)
			}
		})
	}
}

// TestToolCallPublishRejectsForbiddenTopic covers the new MCP
// publish allowlist: homed_publish must refuse anything that is
// not a command/ or td/ topic. The fake client.Publish must NOT
// be called when the topic is rejected.
//
// Note: 'device/...' is intentionally NOT in the rejected list
// because toolPublish auto-rewrites it to 'td/...' (a valid
// publish target). That rewrite is tested in
// TestToolCallPublishAllowsCommandAndTd.
func TestToolCallPublishRejectsForbiddenTopic(t *testing.T) {
	cases := []struct {
		name  string
		topic string
	}{
		{"status", "status/custom/garland_window"},
		{"expose", "expose/custom/garland_window"},
		{"service", "service/custom"},
		{"fd", "fd/custom/garland_window"},
		{"response", "response/whatever"},
		{"no-prefix", "whatever/x/y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeClient{prefix: "homed"}
			frames := []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"homed_publish","arguments":{"topic":` + jsonString(c.topic) + `,"message":{"x":1}}}}`,
			}
			data := runServer(t, fc, frames...)
			resps := parseResponses(t, data)
			if len(resps) != 2 {
				t.Fatalf("want 2 responses, got %d", len(resps))
			}
			var call CallToolResult
			mustMarshal(t, resps[1].Result, &call)
			if !call.IsError {
				t.Fatalf("expected IsError=true for forbidden topic %q, got %+v", c.topic, call)
			}
			if len(fc.published) != 0 {
				t.Errorf("expected no MQTT publish for forbidden topic %q, got %v", c.topic, fc.published)
			}
		})
	}
}

// TestToolCallPublishAllowsCommandAndTd verifies the positive
// path of the new allowlist: command/<svc> and td/<svc>/<id>
// topics are accepted by homed_publish.
func TestToolCallPublishAllowsCommandAndTd(t *testing.T) {
	cases := []struct {
		name      string
		topic     string
		wantTopic string
	}{
		{"command", "command/custom", "command/custom"},
		{"td with instance", "td/custom/instance1/26067540-57076820", "td/custom/instance1/26067540-57076820"},
		{"device rewritten to td", "device/custom/26067540-57076820", "td/custom/26067540-57076820"},
		{"bare shorthand", "custom/26067540-57076820", "td/custom/26067540-57076820"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeClient{prefix: "homed"}
			frames := []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"homed_publish","arguments":{"topic":` + jsonString(c.topic) + `,"message":{"x":1}}}}`,
			}
			data := runServer(t, fc, frames...)
			_ = parseResponses(t, data)
			if len(fc.published) != 1 {
				t.Fatalf("expected 1 publish, got %d (%v)", len(fc.published), fc.published)
			}
			if fc.published[0] != c.wantTopic {
				t.Errorf("topic=%q, want %q", fc.published[0], c.wantTopic)
			}
		})
	}
}

// TestToolCallPublishExplicitTD verifies that an explicit
// "td/..." topic is published as-is and forces retain=false even
// when the caller tried to set retain=true (the HOMEd td/ topic
// must never be retained).
func TestToolCallPublishExplicitTD(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_publish","arguments":{"topic":"td/custom/alarm","message":{"status":"on"},"retained":true}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/custom/alarm" {
		t.Errorf("expected publish to td/custom/alarm, got %v", fc.published)
	}
	if len(fc.publishedRetain) != 1 || fc.publishedRetain[0] != false {
		t.Errorf("expected retain=false for td/ topic, got %v", fc.publishedRetain)
	}
}

// TestToolCallSetDevice verifies the high-level helper: it must
// build a {property:value} payload and publish it to the HOMEd
// "td/" topic without retain. This is the exact command format
// the official homed-web UI uses when a user toggles a switch.
func TestToolCallSetDevice(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"custom/alarm","property":"status","value":"on"}}}`,
	}
	data := runServer(t, fc, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d", len(resps))
	}
	if len(fc.published) != 1 || fc.published[0] != "td/custom/alarm" {
		t.Errorf("expected publish to td/custom/alarm, got %v", fc.published)
	}
	if len(fc.publishedRetain) != 1 || fc.publishedRetain[0] != false {
		t.Errorf("expected retain=false, got %v", fc.publishedRetain)
	}
	if len(fc.publishedPayload) != 1 {
		t.Fatalf("expected one payload, got %d", len(fc.publishedPayload))
	}
	var got map[string]any
	if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
		t.Fatalf("payload is not JSON object: %v / %q", err, fc.publishedPayload[0])
	}
	if got["status"] != "on" {
		t.Errorf("payload status=%v, want on", got["status"])
	}
}

// TestToolCallSetDeviceRawMessage verifies that the "message"
// argument overrides the default {property:value} shape.
func TestToolCallSetDeviceRawMessage(t *testing.T) {
	fc := &fakeClient{prefix: "homed"}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"zigbee/0xabc/1","message":{"state":"OFF","transition":3}}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/zigbee/0xabc/1" {
		t.Errorf("expected publish to td/zigbee/0xabc/1, got %v", fc.published)
	}
	var got map[string]any
	if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
		t.Fatalf("payload is not JSON object: %v / %q", err, fc.publishedPayload[0])
	}
	if got["state"] != "OFF" || got["transition"] != float64(3) {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestPathMatch(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"#", "a/b/c", true},
		{"device/#", "device/light/kitchen", true},
		{"device/#", "expose/light/kitchen", false},
		{"device/+/state", "device/light/state", true},
		{"device/+/state", "device/light/kitchen/state", false},
	}
	for _, c := range cases {
		got, err := pathMatch(c.pattern, c.topic)
		if err != nil {
			t.Errorf("pathMatch(%q,%q): %v", c.pattern, c.topic, err)
			continue
		}
		if got != c.want {
			t.Errorf("pathMatch(%q,%q)=%v want %v", c.pattern, c.topic, got, c.want)
		}
	}
}

func TestResolvePropertyFromExpose(t *testing.T) {
	cases := []struct {
		name     string
		retain   map[string][]byte
		endpoint string
		property string
		want     string
	}{
		{
			name:     "no expose declaration keeps property",
			endpoint: "custom/Svet",
			property: "switch_13",
			want:     "switch_13",
		},
		{
			name: "common block maps switch to status",
			retain: map[string][]byte{
				"expose/custom/alarm": []byte(`{"common":{"items":["switch"]}}`),
			},
			endpoint: "custom/alarm",
			property: "switch",
			want:     "status",
		},
		{
			name: "endpoints id match returns property field",
			retain: map[string][]byte{
				"expose/custom/Svet": []byte(`{"endpoints":[{"id":"switch_13","property":"status_13","title":"Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р В Р вЂ№Р В Р’В Р Р†Р вЂљРЎв„ўР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В РЎС›Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р Р†Р вЂљРЎв„ўР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р В Р вЂ№Р В Р’В Р В Р РЏР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В РЎС›Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°"}]}`),
			},
			endpoint: "custom/Svet",
			property: "switch_13",
			want:     "status_13",
		},
		{
			name: "endpoints id match without property field keeps id",
			retain: map[string][]byte{
				"expose/custom/X": []byte(`{"endpoints":[{"id":"switch_1","title":"x"}]}`),
			},
			endpoint: "custom/X",
			property: "switch_1",
			want:     "switch_1",
		},
		{
			name: "endpoints id mismatch keeps property",
			retain: map[string][]byte{
				"expose/custom/X": []byte(`{"endpoints":[{"id":"switch_2","property":"status_2"}]}`),
			},
			endpoint: "custom/X",
			property: "switch_3",
			want:     "switch_3",
		},
		// Regression: when the caller passes the generic 'status'
		// (i.e. they do not know which channel the device uses)
		// and the device exposes a single multi-channel-style item
		// ('switch_1'), the resolver must default to 'status_1'
		// rather than returning 'status' verbatim. This was
		// reproduced in production with custom/garland_window on
		// 2026-06-08: the wire payload 'status:on' was silently
		// dropped by the custom service, leaving the device off.
		{
			name: "multi-channel single item: generic 'status' defaults to 'status_1'",
			retain: map[string][]byte{
				"expose/custom/garland_window": []byte(`{"common":{"items":["switch_1"]}}`),
			},
			endpoint: "custom/garland_window",
			property: "status",
			want:     "status_1",
		},
		// Companion case: a truly single-channel device keeps
		// the wire-property as 'status' (no underscore prefix in
		// any common item).
		{
			name: "single-channel single item: generic 'status' stays 'status'",
			retain: map[string][]byte{
				"expose/custom/alarm": []byte(`{"common":{"items":["switch"]}}`),
			},
			endpoint: "custom/alarm",
			property: "status",
			want:     "status",
		},
		// Multi-channel device with several switch_N items:
		// the resolver should still default to status_1 when
		// the caller did not pick a specific channel.
		{
			name: "multi-channel many items: generic 'status' defaults to 'status_1'",
			retain: map[string][]byte{
				"expose/custom/Svet": []byte(`{"common":{"items":["switch_1","switch_2","switch_3"]}}`),
			},
			endpoint: "custom/Svet",
			property: "status",
			want:     "status_1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeClient{prefix: "homed", retained: c.retain}
			got := resolvePropertyFromExpose(fc, c.endpoint, c.property)
			if got != c.want {
				t.Errorf("resolvePropertyFromExpose(%q,%q) = %q, want %q", c.endpoint, c.property, got, c.want)
			}
		})
	}
}

// TestToolCallSetDeviceGarlandRegression is the end-to-end
// regression test for the multi-channel single-channel-shape
// scenario. The test reproduces the production call
//
//	homed_set_device(endpoint=custom/garland_window,
//	                 property=status, value=on)
//
// that the live broker silently dropped before the fix, and
// asserts the wire payload now uses 'status_1' as the key.
func TestToolCallSetDeviceGarlandRegression(t *testing.T) {
	fc := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"expose/custom/garland_window": []byte(`{"common":{"items":["switch_1"]}}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"custom/garland_window","property":"status","value":"on"}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/custom/garland_window" {
		t.Fatalf("expected publish to td/custom/garland_window, got %v", fc.published)
	}
	var got map[string]any
	if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
		t.Fatalf("payload is not JSON object: %v / %q", err, fc.publishedPayload[0])
	}
	if got["status_1"] != "on" {
		t.Errorf("payload status_1=%v, want on (regression: must be status_1, not status)", got["status_1"])
	}
	if _, ok := got["status"]; ok {
		t.Errorf("payload must not contain generic 'status' key for a switch_1-only device: %+v", got)
	}
}

// TestToolCallGetTopicStatusFallsBackToDevice reproduces the live
// call from 2026-06-08:
//
//	homed_get_topic(topic="status/custom/garland_window")
//
// Single-channel switch exposes in the HOMEd custom service do
// not publish a retained 'status/<id>' payload, so the naive
// lookup would return 'not found'. The fix in toolGetTopic
// transparently falls back to the always-present
// 'device/<service>/<id>' retain payload, which carries the
// device's online/offline status and the names-aware id/name
// fields. A 'note' field in the response tells the caller about
// the substitution.
func TestToolCallGetTopicStatusFallsBackToDevice(t *testing.T) {
	fc := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"device/custom/garland_window": []byte(`{"status":"online"}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"homed_get_topic","arguments":{"topic":"status/custom/garland_window"}}}`,
	}
	data := runServer(t, fc, frames...)
	resps := parseResponses(t, data)
	if len(resps) != 2 {
		t.Fatalf("want 2 responses, got %d: %s", len(resps), data)
	}
	var call CallToolResult
	mustMarshal(t, resps[1].Result, &call)
	if call.IsError {
		t.Fatalf("get_topic returned an error: %+v", call)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(call.Content[0].Text), &out); err != nil {
		t.Fatalf("text is not JSON object: %v / %q", err, call.Content[0].Text)
	}
	if got, _ := out["topic"].(string); got != "status/custom/garland_window" {
		t.Errorf("topic=%q, want status/custom/garland_window", got)
	}
	if _, ok := out["note"].(string); !ok {
		t.Errorf("expected a 'note' field documenting the fallback; got %+v", out)
	}
}

// TestToolCallGetRequestRejectsReadOnlyTopic reproduces the live
// call from 2026-06-08 18:52:03 that spent 10s waiting for a
// 'response/<id>' that never came:
//
//	homed_get_request(topic="status/custom", timeout=10)
//
// status/, expose/, device/, service/, fd/ and td/ topics are
// retained/event topics and do not answer the request/reply
// pattern. The tool now rejects these prefixes with a clear
// hint pointing at homed_get_topic / homed_get_properties.
func TestToolCallGetRequestRejectsReadOnlyTopic(t *testing.T) {
	cases := []string{
		"status/custom",
		"status/custom/garland_window",
		"expose/custom/garland_window",
		"device/custom/garland_window",
		"service/custom",
		"fd/custom/garland_window",
		"td/custom/garland_window",
	}
	for _, topic := range cases {
		t.Run(topic, func(t *testing.T) {
			fc := &fakeClient{prefix: "homed"}
			frames := []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"homed_get_request","arguments":{"topic":` + jsonString(topic) + `,"timeout":1}}}`,
			}
			data := runServer(t, fc, frames...)
			resps := parseResponses(t, data)
			if len(resps) != 2 {
				t.Fatalf("want 2 responses, got %d", len(resps))
			}
			var call CallToolResult
			mustMarshal(t, resps[1].Result, &call)
			if !call.IsError {
				t.Errorf("expected IsError=true for read-only topic %q, got %+v", topic, call)
			}
			// The fake client's Request should NOT have been
			// called Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ we rejected the topic before the call.
			if len(fc.published) != 0 {
				t.Errorf("expected no MQTT publish for read-only topic %q, got %v", topic, fc.published)
			}
		})
	}
}

// jsonString returns a JSON-quoted Go string literal that can be
// interpolated into a JSON-RPC frame. Used to keep the test
// above readable.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestToolCallSetDeviceResolvesProperty(t *testing.T) {
	fc := &fakeClient{
		prefix: "homed",
		retained: map[string][]byte{
			"expose/custom/Svet": []byte(`{"endpoints":[{"id":"switch_13","property":"status_13","title":"Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р Р†РІР‚С›РЎС›Р В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’ВµР В Р’В Р В Р вЂ№Р В Р’В Р Р†Р вЂљРЎв„ўР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В РЎС›Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°Р В Р’В Р вЂ™Р’В Р В Р вЂ Р В РІР‚С™Р РЋРЎв„ўР В Р’В Р вЂ™Р’В Р В Р Р‹Р Р†Р вЂљР’ВР В Р’В Р В Р вЂ№Р В Р’В Р Р†Р вЂљРЎв„ўР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В»Р В Р’В Р В Р вЂ№Р В Р’В Р В Р РЏР В Р’В Р вЂ™Р’В Р В Р’В Р Р†Р вЂљР’В¦Р В Р’В Р вЂ™Р’В Р В РЎС›Р Р†Р вЂљР’ВР В Р’В Р вЂ™Р’В Р В РІР‚в„ўР вЂ™Р’В°"}]}`),
		},
	}
	frames := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":"custom/Svet","property":"switch_13","value":"on"}}}`,
	}
	data := runServer(t, fc, frames...)
	_ = parseResponses(t, data)
	if len(fc.published) != 1 || fc.published[0] != "td/custom/Svet" {
		t.Fatalf("expected publish to td/custom/Svet, got %v", fc.published)
	}
	var got map[string]any
	if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
		t.Fatalf("payload is not JSON object: %v / %q", err, fc.publishedPayload[0])
	}
	if got["status_13"] != "on" {
		t.Errorf("payload=%+v, want status_13=on", got)
	}
	if _, ok := got["switch_13"]; ok {
		t.Errorf("payload must not contain switch_13: %+v", got)
	}
}

// TestToolCallSetDeviceUnconditionalSwitchToStatus verifies the
// hard convention reported by the user: regardless of the cached
// expose declaration, every 'switch' / 'switch_N' property in the
// payload is rewritten to 'status' / 'status_N'. This is the
// wire format that homed-web actually consumes for custom-*
// switch exposes (e.g. td/custom/26067540-57076820 = {"status_1":"on"}).
func TestToolCallSetDeviceUnconditionalSwitchToStatus(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		property string
		wantKey  string
	}{
		{"single-channel switch", "custom/garland_window", "switch_1", "status_1"},
		{"multi-channel switch_13", "custom/26067540-57076820", "switch_1", "status_1"},
		{"plain switch", "custom/alarm", "switch", "status"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &fakeClient{prefix: "homed"} // empty retained cache
			frames := []string{
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}`,
				`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
				fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"homed_set_device","arguments":{"endpoint":%q,"property":%q,"value":"on"}}}`, c.endpoint, c.property),
			}
			data := runServer(t, fc, frames...)
			_ = parseResponses(t, data)
			if len(fc.published) != 1 {
				t.Fatalf("expected 1 publish, got %d", len(fc.published))
			}
			if fc.published[0] != "td/"+c.endpoint {
				t.Errorf("topic=%q, want td/%s", fc.published[0], c.endpoint)
			}
			var got map[string]any
			if err := json.Unmarshal(fc.publishedPayload[0], &got); err != nil {
				t.Fatalf("payload is not JSON: %v / %q", err, fc.publishedPayload[0])
			}
			if got[c.wantKey] != "on" {
				t.Errorf("payload=%+v, want %s=on", got, c.wantKey)
			}
			if len(got) != 1 {
				t.Errorf("payload must contain exactly one key, got %+v", got)
			}
		})
	}
}

// runServerWithMeta is the variant of runServer that wires a custom
// MetaSource so individual tests can verify the user-defined
// name enrichment.
func runServerWithMeta(t *testing.T, client MQTTClient, meta MetaSource, frames ...string) []byte {
	t.Helper()
	srv := NewServer("test", "0.0.0")
	RegisterHOMEdTools(srv, client, meta)
	in := strings.NewReader(strings.Join(frames, "\n") + "\n")
	out := &bytes.Buffer{}
	if err := srv.Run(context.Background(), in, out); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out.Bytes()
}

func mustMarshal(t *testing.T, in any, out any) {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func fmtSprint(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}