// Package mqtt provides a wrapper around the Eclipse Paho MQTT client tuned
// for the HOMEd protocol conventions (topic prefix, retained discovery, etc.).
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/u236/homed-mcp/internal/logger"
)

// Config holds connection parameters.
type Config struct {
	Broker   string
	ClientID string
	Username string
	Password string
	Prefix   string
	// Logger is the structured logger the client should write
	// diagnostic / trace information to. A nil logger disables
	// MQTT-side logging entirely.
	Logger *logger.Logger
}

// Client is a thin wrapper around the Paho client providing helpers
// tailored for HOMEd topics.
type Client struct {
	cfg    Config
	client paho.Client
	log    *logger.Logger

	mu          sync.Mutex
	pending     map[string]chan []byte            // correlation id -> response channel
	retained    map[string][]byte                 // topic -> last retained payload
	live        map[string][]byte                 // topic -> last non-retained payload for explicit subs
	subs        map[string]bool                   // active sub-topics that should populate `live`
	liveWaiters map[string][]func(string, []byte) // sub-topic matcher -> delivery callbacks
	// retainedHooks are invoked (synchronously, on the MQTT
	// dispatcher goroutine) for every retained message that lands
	// in the cache, after the cache itself has been updated.
	// They run under no lock вЂ” implementations must be quick and
	// must not call back into the Client in a way that re-enters
	// the cache. Used by the alias resolver to invalidate its
	// snapshot without polling Retained() on every lookup.
	retainedHooks []func(topic string, payload []byte)
	// serviceNames records, per HOMEd service, the value of the
	// 'names' boolean published on the retain topic
	// {prefix}/status/{service}. When true, the service publishes
	// its own device 'name' in MQTT topic paths (e.g.
	// expose/zigbee/OpenTherm) instead of the device 'id' (e.g.
	// expose/zigbee/0x00124b0014b0b0b0). Knowing which convention a
	// service uses is essential for the MCP tools to address the
	// correct topic and to interpret the cached payloads.
	//
	// The map is populated from the retain message
	// {prefix}/status/{service} which the service itself
	// republishes whenever its 'names' setting changes. When no
	// flag has been seen yet (or it was last seen as 'false' / a
	// payload without the key) the map entry is either absent or
	// maps to false вЂ” i.e. 'use the device id'. This matches the
	// historical HOMEd default.
	serviceNames map[string]bool
}

// OnRetained registers a callback that is invoked for every
// retained message that lands in the cache (including the
// initial flurry of retains right after Subscribe). The hook
// runs synchronously on the MQTT dispatcher goroutine, after
// the cache has been updated, with no Client lock held.
//
// The hook is intended for in-process observers that need to
// react to retained-state changes (e.g. invalidating a cached
// alias map). Implementations must be cheap and must not
// re-enter the Client from inside the callback. The function
// is safe to call before or after Connect().
func (c *Client) OnRetained(hook func(topic string, payload []byte)) {
	if hook == nil {
		return
	}
	c.mu.Lock()
	c.retainedHooks = append(c.retainedHooks, hook)
	c.mu.Unlock()
}

// NewClient connects to the broker and starts a goroutine that fills the
// retained cache.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Prefix == "" {
		cfg.Prefix = "homed"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "homed-mcp-" + time.Now().Format("20060102150405")
	}

	opts := paho.NewClientOptions().
		AddBroker(cfg.Broker).
		SetClientID(cfg.ClientID).
		SetUsername(cfg.Username).
		SetPassword(cfg.Password).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetMaxReconnectInterval(30 * time.Second)

	c := &Client{
		cfg:          cfg,
		log:          cfg.Logger,
		pending:      make(map[string]chan []byte),
		retained:     make(map[string][]byte),
		live:         make(map[string][]byte),
		subs:         make(map[string]bool),
		liveWaiters:  make(map[string][]func(string, []byte)),
		serviceNames: make(map[string]bool),
	}

	if c.log != nil {
		c.log.Infof("mqtt: connecting to %s as %s (prefix=%s)", cfg.Broker, cfg.ClientID, cfg.Prefix)
	}

	opts.SetDefaultPublishHandler(c.onMessage)

	client := paho.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		if c.log != nil {
			c.log.Infof("mqtt: connection to %s timed out", cfg.Broker)
		}
		return nil, fmt.Errorf("mqtt: connection timeout")
	}
	if err := tok.Error(); err != nil {
		if c.log != nil {
			c.log.Infof("mqtt: connection to %s failed: %s", cfg.Broker, err)
		}
		return nil, fmt.Errorf("mqtt: connect: %w", err)
	}
	c.client = client
	if c.log != nil {
		c.log.Infof("mqtt: connected to %s", cfg.Broker)
	}
	return c, nil
}

// Prefix returns the configured HOMEd topic prefix.
func (c *Client) Prefix() string { return c.cfg.Prefix }

// Topic builds a full HOMEd topic from a sub-topic.
func (c *Client) Topic(sub string) string {
	return c.cfg.Prefix + "/" + sub
}

// Subscribe subscribes to a sub-topic (e.g. "status/#" or "device/light/kitchen").
// The sub-topic is automatically registered in the live cache so that
// subsequent non-retained messages on matching topics are observable via
// Live() and WaitFor().
func (c *Client) Subscribe(sub string, qos byte) error {
	topic := c.Topic(sub)
	if c.log != nil {
		c.log.Debugf("mqtt: subscribe %s (qos=%d)", topic, qos)
	}
	tok := c.client.Subscribe(topic, qos, nil)
	if !tok.WaitTimeout(10 * time.Second) {
		if c.log != nil {
			c.log.Debugf("mqtt: subscribe %s: timeout", topic)
		}
		return fmt.Errorf("mqtt: subscribe %q: timeout", topic)
	}
	if err := tok.Error(); err != nil {
		if c.log != nil {
			c.log.Debugf("mqtt: subscribe %s: %s", topic, err)
		}
		return err
	}
	c.mu.Lock()
	c.subs[sub] = true
	c.mu.Unlock()
	return nil
}

// Unsubscribe removes a subscription and unregisters the live-cache tracking.
func (c *Client) Unsubscribe(sub string) error {
	topic := c.Topic(sub)
	if c.log != nil {
		c.log.Debugf("mqtt: unsubscribe %s", topic)
	}
	tok := c.client.Unsubscribe(topic)
	if !tok.WaitTimeout(5 * time.Second) {
		if c.log != nil {
			c.log.Debugf("mqtt: unsubscribe %s: timeout", topic)
		}
		return fmt.Errorf("mqtt: unsubscribe %q: timeout", topic)
	}
	if err := tok.Error(); err != nil {
		if c.log != nil {
			c.log.Debugf("mqtt: unsubscribe %s: %s", topic, err)
		}
		return err
	}
	c.mu.Lock()
	delete(c.subs, sub)
	c.mu.Unlock()
	return nil
}

// Live returns a snapshot of the latest non-retained payloads received for
// topics that match an active sub-topic registered via Subscribe(). It does
// not include retained payloads; for those use Retained().
func (c *Client) Live() map[string][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]byte, len(c.live))
	for k, v := range c.live {
		out[k] = v
	}
	return out
}

// WaitFor subscribes to sub (if not already) and waits for the first
// incoming message on any topic matching sub. MQTT wildcards '+' and '#' are
// supported. The returned bytes are the raw payload. The call is cancelled
// when ctx is done or when the timeout elapses.
func (c *Client) WaitFor(ctx context.Context, sub string, timeout time.Duration) (string, []byte, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if c.log != nil {
		c.log.Debugf("mqtt: WaitFor %s (timeout=%s)", sub, timeout)
	}
	if err := c.Subscribe(sub, 1); err != nil {
		return "", nil, fmt.Errorf("mqtt: subscribe %q: %w", sub, err)
	}

	ch := make(chan struct {
		topic   string
		payload []byte
	}, 1)

	c.mu.Lock()
	c.liveWaiters[sub] = append(c.liveWaiters[sub], func(topic string, payload []byte) {
		select {
		case ch <- struct {
			topic   string
			payload []byte
		}{topic, payload}:
		default:
		}
	})
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		waiters := c.liveWaiters[sub]
		for i, w := range waiters {
			// identity compare is not possible in Go; remove the matching
			// closure by relying on the single-use channel: since we
			// delivered already, we just drop the last waiter.
			_ = w
			_ = i
		}
		if len(waiters) > 0 {
			c.liveWaiters[sub] = waiters[:0]
		}
		c.mu.Unlock()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case msg := <-ch:
		if c.log != nil {
			c.log.Debugf("mqtt: WaitFor %s -> %s (%d bytes)", sub, msg.topic, len(msg.payload))
		}
		return msg.topic, msg.payload, nil
	case <-ctx.Done():
		if c.log != nil {
			c.log.Debugf("mqtt: WaitFor %s: ctx done (%s)", sub, ctx.Err())
		}
		return "", nil, ctx.Err()
	case <-timer.C:
		if c.log != nil {
			c.log.Debugf("mqtt: WaitFor %s: timeout", sub)
		}
		return "", nil, fmt.Errorf("mqtt: wait %q: timeout", sub)
	}
}

// Publish sends a JSON payload to a sub-topic.
func (c *Client) Publish(sub string, payload any, retained bool) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("mqtt: marshal payload: %w", err)
	}
	topic := c.Topic(sub)
	if c.log != nil {
		if len(data) > 256 {
			c.log.Debugf("mqtt: publish %s (retained=%v) payload=%d bytes", topic, retained, len(data))
		} else {
			c.log.Debugf("mqtt: publish %s (retained=%v) payload=%s", topic, retained, string(data))
		}
	}
	tok := c.client.Publish(topic, 0, retained, data)
	if !tok.WaitTimeout(10 * time.Second) {
		if c.log != nil {
			c.log.Debugf("mqtt: publish %s: timeout", topic)
		}
		return fmt.Errorf("mqtt: publish %q: timeout", topic)
	}
	if err := tok.Error(); err != nil {
		if c.log != nil {
			c.log.Debugf("mqtt: publish %s: %s", topic, err)
		}
		return err
	}
	return nil
}

// Retained returns a snapshot of cached retained payloads.
func (c *Client) Retained() map[string][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]byte, len(c.retained))
	for k, v := range c.retained {
		out[k] = v
	}
	return out
}

// Request publishes a "get" style command and waits for the matching response
// on response/<id>. The HOMEd convention is to use the response sub-topic
// `<prefix>/response/<id>` for a request with `id=<id>`.
func (c *Client) Request(ctx context.Context, sub string, payload map[string]any) (json.RawMessage, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if c.log != nil {
		c.log.Debugf("mqtt: request %s id=%s", sub, id)
	}

	ch := make(chan []byte, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.Subscribe("response/"+id, 1); err != nil {
		return nil, fmt.Errorf("mqtt: subscribe response: %w", err)
	}
	defer c.Unsubscribe("response/" + id)

	if payload == nil {
		payload = map[string]any{}
	}
	payload["id"] = id

	if err := c.Publish(sub, payload, false); err != nil {
		return nil, err
	}

	select {
	case data := <-ch:
		if c.log != nil {
			if len(data) > 256 {
				c.log.Debugf("mqtt: request %s id=%s -> %d bytes", sub, id, len(data))
			} else {
				c.log.Debugf("mqtt: request %s id=%s -> %s", sub, id, string(data))
			}
		}
		return data, nil
	case <-ctx.Done():
		if c.log != nil {
			c.log.Debugf("mqtt: request %s id=%s: ctx done (%s)", sub, id, ctx.Err())
		}
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		if c.log != nil {
			c.log.Debugf("mqtt: request %s id=%s: timeout", sub, id)
		}
		return nil, fmt.Errorf("mqtt: request %q: timeout", sub)
	}
}

func (c *Client) onMessage(_ paho.Client, msg paho.Message) {
	topic := msg.Topic()
	prefix := c.cfg.Prefix + "/"
	if len(topic) > len(prefix) && topic[:len(prefix)] == prefix {
		topic = topic[len(prefix):]
	}

	payload := append([]byte(nil), msg.Payload()...)

	if msg.Retained() {
		c.mu.Lock()
		if len(payload) == 0 {
			delete(c.retained, topic)
		} else {
			c.retained[topic] = payload
		}
		// Take a copy of the hook slice under the lock so we can
		// invoke the hooks outside of it. Hooks must not call back
		// into the Client in a way that re-acquires c.mu.
		hooks := append([]func(string, []byte){}, c.retainedHooks...)
		c.mu.Unlock()
		// Capture per-service 'names' flag published on
		// {prefix}/status/{service}. The payload is a small JSON
		// object with a 'names' boolean that tells downstream
		// tools whether the service uses device names or device
		// ids in MQTT topic paths.
		c.captureServiceNamesFlag(topic, payload)
		// Log the full retained payload on debug so that the on-disk
		// journal mirrors the same view the live cache uses.
		if c.log != nil {
			if len(payload) == 0 {
				c.log.Debugf("mqtt: <-- %s (retained, deleted)", topic)
			} else if len(payload) > 512 {
				c.log.Debugf("mqtt: <-- %s (retained, %d bytes) payload=%s...", topic, len(payload), truncateString(string(payload), 512))
			} else {
				c.log.Debugf("mqtt: <-- %s (retained) payload=%s", topic, string(payload))
			}
		}
		// Fire retained-state observers (e.g. the alias resolver
		// snapshot invalidator) outside the lock.
		for _, h := range hooks {
			h(topic, payload)
		}
	}

	if len(topic) > len("response/") && topic[:len("response/")] == "response/" {
		id := topic[len("response/"):]
		c.mu.Lock()
		ch, ok := c.pending[id]
		c.mu.Unlock()
		if ok {
			select {
			case ch <- payload:
			default:
			}
		}
	}

	// Cache non-retained messages for any active sub-topic whose filter
	// matches this topic, and deliver to WaitFor() callers.
	c.mu.Lock()
	var matches []func(string, []byte)
	for sub := range c.subs {
		if topicMatchesMQTT(sub, topic) {
			c.live[topic] = payload
			if waiters, ok := c.liveWaiters[sub]; ok {
				matches = append(matches, waiters...)
				delete(c.liveWaiters, sub)
			}
		}
	}
	c.mu.Unlock()
	for _, cb := range matches {
		cb(topic, payload)
	}
}

// topicMatchesMQTT is a minimal MQTT-style matcher used to decide whether a
// non-retained message should be stored in the live cache and delivered to
// waiters. It supports the multi-level wildcard '#' and the single-level
// wildcard '+'.
func topicMatchesMQTT(filter, topic string) bool {
	if filter == topic {
		return true
	}
	// '#' must be the last segment and matches the remainder of the topic
	// at a segment boundary (i.e. the prefix before '#' must end with a
	// slash, or '#' must be at the very start of the filter).
	if i := strings.IndexByte(filter, '#'); i >= 0 {
		if i != len(filter)-1 {
			return false
		}
		prefix := filter[:i]
		if prefix == "" {
			return true
		}
		// prefix must end with '/' to mark a segment boundary.
		if prefix[len(prefix)-1] != '/' {
			return false
		}
		return strings.HasPrefix(topic, prefix)
	}
	// Segment-level match with '+' wildcards.
	fSegs := strings.Split(filter, "/")
	tSegs := strings.Split(topic, "/")
	if len(fSegs) != len(tSegs) {
		return false
	}
	for i, s := range fSegs {
		if s == "+" {
			continue
		}
		if s != tSegs[i] {
			return false
		}
	}
	return true
}

// Disconnect cleanly shuts down the underlying client.
func (c *Client) Disconnect() {
	if c.client == nil {
		return
	}
	if c.log != nil {
		c.log.Infof("mqtt: disconnecting from %s", c.cfg.Broker)
	}
	c.client.Disconnect(500)
}

// truncateString returns the first n bytes of s, appending "..." when the
// string was actually clipped. It is used to keep single log lines from
// exploding when a retained payload happens to be very large.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// captureServiceNamesFlag inspects a retained payload that just
// landed in the cache and, when it is published on
// 'status/<service>', records the 'names' boolean for that service
// in the serviceNames map. Payloads that do not look like a service
// status, or whose JSON does not parse, are silently ignored вЂ” the
// flag is purely advisory and a missing entry is treated as
// 'use device id' (the historical HOMEd default).
//
// A retained empty payload (a 'delete' on the broker) is honoured
// by clearing any previously recorded flag for that service so the
// client does not get stuck in a stale state.
func (c *Client) captureServiceNamesFlag(topic string, payload []byte) {
	const statusPrefix = "status/"
	if len(topic) <= len(statusPrefix) || topic[:len(statusPrefix)] != statusPrefix {
		return
	}
	service := topic[len(statusPrefix):]
	// Defensive guard: a service id must be non-empty and must
	// not contain a slash (so that 'status/zigbee/foo' is not
	// mistaken for a service id of 'zigbee/foo').
	if service == "" || strings.ContainsRune(service, '/') {
		return
	}
	if len(payload) == 0 {
		c.mu.Lock()
		delete(c.serviceNames, service)
		c.mu.Unlock()
		return
	}
	var raw struct {
		Names *bool `json:"names"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		// Malformed JSON is not fatal вЂ” leave the previous value
		// (if any) untouched.
		return
	}
	c.mu.Lock()
	if raw.Names == nil {
		// A 'names' field is not present in this payload.
		// Persist the most informative value we can: explicit
		// 'false' so that a future 'names: true' message
		// overrides it cleanly. If the service was previously
		// recorded as 'true' (e.g. via a newer payload that was
		// already seen), keep that вЂ” a stripped payload never
		// regresses the flag.
		if _, seen := c.serviceNames[service]; !seen {
			c.serviceNames[service] = false
		}
	} else {
		c.serviceNames[service] = *raw.Names
	}
	c.mu.Unlock()
}

// ServiceUsesNames reports whether the given HOMEd service uses
// device 'name' (true) or device 'id' (false) in MQTT topic
// paths. The default is false (the historical HOMEd convention)
// when no 'status/<service>' payload has been seen yet. The
// boolean is taken from the retain topic
// {prefix}/status/<service> which the service republishes whenever
// its 'names' setting changes.
func (c *Client) ServiceUsesNames(service string) bool {
	if service == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.serviceNames[service]
}

// ServiceNames returns a copy of the per-service 'names' map.
// The map keys are HOMEd service names (zigbee, matter, ble,
// custom, ...) and the values are the boolean read from
// {prefix}/status/<service>. Callers must treat the result as
// read-only.
func (c *Client) ServiceNames() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.serviceNames))
	for k, v := range c.serviceNames {
		out[k] = v
	}
	return out
}
