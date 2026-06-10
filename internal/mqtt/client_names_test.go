package mqtt

import "testing"

// TestServiceUsesNamesDefault verifies the default behaviour: a
// fresh Client reports false for every service (the historical
// HOMEd convention) until a retain payload is observed.
func TestServiceUsesNamesDefault(t *testing.T) {
	c := &Client{}
	if c.ServiceUsesNames("zigbee") {
		t.Errorf("default ServiceUsesNames(zigbee) = true, want false")
	}
	if c.ServiceUsesNames("custom") {
		t.Errorf("default ServiceUsesNames(custom) = true, want false")
	}
	if c.ServiceUsesNames("") {
		t.Errorf("default ServiceUsesNames(\"\") = true, want false")
	}
}

// TestServiceNamesDefault verifies the snapshot is empty by
// default.
func TestServiceNamesDefault(t *testing.T) {
	c := &Client{}
	if got := c.ServiceNames(); len(got) != 0 {
		t.Errorf("default ServiceNames() = %v, want empty map", got)
	}
}

// TestCaptureServiceNamesFlagTrue covers the happy path for the
// capture function: a 'status/<service>' retain payload with
// "names": true flips the flag.
func TestCaptureServiceNamesFlagTrue(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"names":true}`))
	if !c.ServiceUsesNames("custom") {
		t.Errorf("ServiceUsesNames(custom) = false, want true")
	}
}

// TestCaptureServiceNamesFlagFalse covers the explicit-false case.
func TestCaptureServiceNamesFlagFalse(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"names":false}`))
	if c.ServiceUsesNames("custom") {
		t.Errorf("ServiceUsesNames(custom) = true, want false")
	}
}

// TestCaptureServiceNamesFlagMissing verifies that a status
// payload without a 'names' key still records the service (with
// false) so that future updates can override it cleanly.
func TestCaptureServiceNamesFlagMissing(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"version":"1.0"}`))
	if c.ServiceUsesNames("custom") {
		t.Errorf("ServiceUsesNames(custom) = true, want false (missing key)")
	}
	if _, ok := c.ServiceNames()["custom"]; !ok {
		t.Errorf("ServiceNames() should record 'custom' even when the payload is missing 'names'")
	}
}

// TestCaptureServiceNamesFlagMalformedJSON verifies that an
// unparseable payload is silently ignored.
func TestCaptureServiceNamesFlagMalformedJSON(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{not json`))
	if _, ok := c.ServiceNames()["custom"]; ok {
		t.Errorf("malformed JSON must not register a service")
	}
}

// TestCaptureServiceNamesFlagEmpty verifies that a retained
// empty payload (a 'delete' on the broker) clears a previously
// recorded flag.
func TestCaptureServiceNamesFlagEmpty(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"names":true}`))
	if !c.ServiceUsesNames("custom") {
		t.Fatalf("setup: expected names=true for custom")
	}
	c.captureServiceNamesFlag("status/custom", []byte{})
	if c.ServiceUsesNames("custom") {
		t.Errorf("ServiceUsesNames(custom) = true after empty retain, want false")
	}
	if _, ok := c.ServiceNames()["custom"]; ok {
		t.Errorf("ServiceNames() should drop 'custom' after empty retain")
	}
}

// TestCaptureServiceNamesFlagNonStatusTopic verifies that the
// capture function is a no-op for topics that are not
// 'status/<service>'.
func TestCaptureServiceNamesFlagNonStatusTopic(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("device/custom/alarm", []byte(`{"names":true}`))
	if names := c.ServiceNames(); len(names) != 0 {
		t.Errorf("ServiceNames() should be empty, got %v", names)
	}
	c.captureServiceNamesFlag("status", []byte(`{"names":true}`)) // no slash
	if names := c.ServiceNames(); len(names) != 0 {
		t.Errorf("ServiceNames() should be empty for 'status' without slash, got %v", names)
	}
	c.captureServiceNamesFlag("status/zigbee/foo", []byte(`{"names":true}`)) // nested
	if names := c.ServiceNames(); len(names) != 0 {
		t.Errorf("ServiceNames() should be empty for 'status/zigbee/foo' (nested), got %v", names)
	}
}

// TestCaptureServiceNamesFlagOverride verifies that a 'true'
// payload overrides a previously seen 'false' / missing payload.
func TestCaptureServiceNamesFlagOverride(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"version":"1.0"}`))
	if c.ServiceUsesNames("custom") {
		t.Fatalf("setup: expected names=false initially")
	}
	c.captureServiceNamesFlag("status/custom", []byte(`{"names":true}`))
	if !c.ServiceUsesNames("custom") {
		t.Errorf("ServiceUsesNames(custom) = false after names:true, want true")
	}
}

// TestServiceNamesSnapshotIsCopy verifies the snapshot returned
// by ServiceNames() cannot mutate the internal state.
func TestServiceNamesSnapshotIsCopy(t *testing.T) {
	c := &Client{serviceNames: map[string]bool{}}
	c.captureServiceNamesFlag("status/custom", []byte(`{"names":true}`))
	snap := c.ServiceNames()
	snap["custom"] = false
	if !c.ServiceUsesNames("custom") {
		t.Errorf("mutating snapshot affected internal state")
	}
	delete(snap, "custom")
	if !c.ServiceUsesNames("custom") {
		t.Errorf("deleting from snapshot affected internal state")
	}
}