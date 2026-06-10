package mqtt

import "testing"

func TestTopicMatchesMQTT(t *testing.T) {
	cases := []struct {
		filter, topic string
		want          bool
	}{
		{"#", "anything", true},
		{"#", "a/b/c", true},
		{"device/#", "device/light/kitchen", true},
		{"device/#", "device/light", true},
		{"device/#", "device", false},
		{"device/#", "expose/light", false},
		{"device/+/state", "device/light/state", true},
		{"device/+/state", "device/light/kitchen/state", false},
		{"a/+/c", "a/b/c", true},
		{"a/+/c", "a/b/d", false},
		{"a/#", "a", false},
		{"a/#", "a/b", true},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"+/+", "a/b", true},
		{"+/+", "a", false},
	}
	for _, c := range cases {
		got := topicMatchesMQTT(c.filter, c.topic)
		if got != c.want {
			t.Errorf("topicMatchesMQTT(%q,%q)=%v want %v", c.filter, c.topic, got, c.want)
		}
	}
}