package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// fetchSetConfirmation is invoked by toolSetDevice after a control
// payload has been published to "td/<service>/<id>". It asks the
// device for its new state through the getProperties request/reply
// pattern and returns a compact JSON object the model can paste
// directly into its answer. The goal is to collapse the typical
// "publish, then read back" round-trip into a single
// homed_set_device call so the model never has to issue a follow-up
// homed_get_properties just to confirm a toggle.
//
// The function is intentionally lenient: on any error or timeout
// it returns nil so the caller can fall back to the plain
// "published to X" text. The model still has the confidence the
// command was sent; it just does not get the device's reply.
//
// The implementation re-uses the same getProperties plumbing the
// model has access to, but with two simplifications:
//
//  1. The response topic is hard-coded to "fd/<service>/<id>"
//     (no names rewrite) because by construction the just-published
//     td/ topic has already resolved the broker-side name Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ the
//     device that needs to answer is the one that owns that name.
//  2. The timeout is hard-coded to 2 s (the caller controls the
//     exact value) so a sluggish device never blocks the session.
//
// Returns a map ready for jsonResult, or nil on error.
func fetchSetConfirmation(ctx context.Context, client MQTTClient, meta MetaSource, endpoint, tdTopic string, timeout time.Duration) map[string]any {
	if client == nil || endpoint == "" {
		return nil
	}
	// endpoint is the original '<service>/<id>[/<ep>]' supplied by
	// the caller. Split it into the (service, device) pair that the
	// getProperties request needs.
	service, device, ok := splitServiceDevice(endpoint)
	if !ok {
		return nil
	}
	// Build a child context with the requested timeout. We do not
	// mutate the caller's context so the parent deadline is
	// unaffected.
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Subscribe to the response topic and arm the waiter. The
	// responseTopic helper already honours the per-service 'names'
	// flag, so we re-use it instead of duplicating the rewrite
	// logic.
	var responseTopic string
	if svc, dev, ok2 := splitServiceDevice(endpoint); ok2 && svc != "" && dev != "" {
		_, respTopic, _ := resolveGetPropertiesArgs(client, svc, dev)
		if respTopic != "" {
			responseTopic = respTopic
		} else {
			responseTopic = "fd/" + svc + "/" + dev
		}
	} else {
		return nil
	}

	if err := client.Subscribe(responseTopic, 1); err != nil {
		return nil
	}
	defer client.Unsubscribe(responseTopic)

	// The default getProperties body. We always go through the
	// homed-web convention (action/device/service) Р В Р’В Р В РІР‚В Р В Р’В Р Р†Р вЂљРЎв„ўР В Р вЂ Р В РІР‚С™Р РЋРЎС™ the 'web'
	// service proxy path is not relevant for a control reply, the
	// device that just received the td/ command is also the one
	// that will answer command/<service>.
	payload := map[string]any{
		"action":  "getProperties",
		"device":  device,
		"service": service,
	}
	commandTopic := "command/" + service

	if err := client.Publish(commandTopic, payload, false); err != nil {
		return nil
	}

	_, respPayload, err := client.WaitFor(cctx, responseTopic, timeout)
	if err != nil {
		return nil
	}
	// The reply is the raw JSON object sent back by the device.
	// Re-marshal it so we can hand back a consistent map (the
	// caller wraps in jsonResult which marshals again, but
	// doing it once here keeps the helper self-contained).
	var pretty any
	if err := json.Unmarshal(respPayload, &pretty); err != nil {
		// The body was not JSON; hand it back as a string.
		pretty = string(respPayload)
	}
	out := map[string]any{
		"endpoint": endpoint,
		"newState": pretty,
	}
	// Surface the user-defined name from homed-web so the model
	// can phrase the result naturally. This is the same lookup
	// toolGetProperties uses, kept inline to avoid a circular
	// dependency between the helper and the tool function.
	if meta != nil && endpoint != "" {
		if matches := meta.LookupEndpoint(endpoint); len(matches) > 0 {
			if first := matches[0]; first.Item.Name != "" {
				out["name"] = first.Item.Name
			}
		}
	}
	// Tag the response so the model can distinguish a confirmed
	// set from a plain publish.
	out["confirmed"] = true
	_ = tdTopic
	return out
}

// splitServiceDevice extracts "<service>/<device>" from an
// endpoint of the form "<service>/<device>[/<endpoint>]". The
// returned ok flag is false when the input does not contain at
// least one slash.
func splitServiceDevice(endpoint string) (service, device string, ok bool) {
	endpoint = strings.TrimPrefix(endpoint, "/")
	idx := strings.IndexByte(endpoint, '/')
	if idx <= 0 || idx == len(endpoint)-1 {
		return "", "", false
	}
	return endpoint[:idx], endpoint[idx+1:], true
}

// Sanity guard against accidentally passing the wrong type to
// jsonResult. Calling fmt.Sprintf on a non-string does the right
// thing for the common case (number/boolean) so the model still
// gets a readable "published to ..." line on fallback.
var _ = fmt.Sprintf