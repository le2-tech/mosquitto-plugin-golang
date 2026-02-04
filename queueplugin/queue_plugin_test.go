package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// withConfig 在单个测试内临时覆盖全局配置。
func withConfig(t *testing.T, c config, fn func()) {
	t.Helper()
	old := cfg
	cfg = c
	t.Cleanup(func() { cfg = old })
	fn()
}

func TestTopicMatch(t *testing.T) {
	cases := []struct {
		pattern string
		topic   string
		want    bool
	}{
		{"#", "a/b/c", true},
		{"a/#", "a", true},
		{"a/#", "a/b/c", true},
		{"a/+", "a/b", true},
		{"a/+", "a/b/c", false},
		{"a/+/c", "a/b/c", true},
		{"a/+/c", "a/b/d", false},
		{"a/b", "a/b", true},
		{"a/b", "a/b/c", false},
		{"a/#/b", "a/x/b", false},
	}

	for _, tc := range cases {
		if got := topicMatch(tc.pattern, tc.topic); got != tc.want {
			t.Fatalf("topicMatch(%q, %q) = %v, want %v", tc.pattern, tc.topic, got, tc.want)
		}
	}
}

func TestAllowMessageDefaults(t *testing.T) {
	withConfig(t, config{
		excludeTopics:   []string{"$SYS/#"},
		includeRetained: false,
	}, func() {
		if allow, _ := allowMessage("$SYS/broker/uptime", "", "", false); allow {
			t.Fatal("expected $SYS topic to be excluded")
		}
		if allow, _ := allowMessage("devices/a/up", "", "", true); allow {
			t.Fatal("expected retained message to be excluded by default")
		}
		if allow, _ := allowMessage("devices/a/up", "", "", false); !allow {
			t.Fatal("expected normal message to pass")
		}
	})
}

func TestAllowMessageIncludeExclude(t *testing.T) {
	withConfig(t, config{
		includeTopics:   []string{"devices/+/up"},
		excludeTopics:   []string{"devices/bob/#"},
		includeUsers:    map[string]struct{}{"alice": {}},
		excludeUsers:    map[string]struct{}{"alice": {}},
		includeClients:  map[string]struct{}{"c1": {}},
		excludeClients:  map[string]struct{}{"c1": {}},
		includeRetained: true,
	}, func() {
		if allow, _ := allowMessage("devices/alice/up", "alice", "c1", false); allow {
			t.Fatal("expected exclude to override include for user/client")
		}
		if allow, _ := allowMessage("devices/bob/up", "bob", "c2", false); allow {
			t.Fatal("expected user include to block non-included user")
		}
	})
}

func TestAllowMessageTopicFilters(t *testing.T) {
	withConfig(t, config{
		includeTopics:   []string{"devices/+/up"},
		excludeTopics:   []string{"devices/bob/#"},
		includeRetained: true,
	}, func() {
		if allow, _ := allowMessage("devices/alice/up", "", "", false); !allow {
			t.Fatal("expected included topic to pass")
		}
		if allow, _ := allowMessage("devices/bob/up", "", "", false); allow {
			t.Fatal("expected excluded topic to be blocked")
		}
		if allow, _ := allowMessage("devices/alice/down", "", "", false); allow {
			t.Fatal("expected non-included topic to be blocked")
		}
	})
}

func TestParseListSet(t *testing.T) {
	list := parseList(" a, b ,,c ")
	if len(list) != 3 || list[0] != "a" || list[1] != "b" || list[2] != "c" {
		t.Fatalf("parseList unexpected result: %#v", list)
	}

	set := parseSet("x, y")
	if len(set) != 2 {
		t.Fatalf("parseSet unexpected size: %d", len(set))
	}
	if _, ok := set["x"]; !ok {
		t.Fatal("parseSet missing x")
	}
}

func TestParseFailMode(t *testing.T) {
	if mode, ok := parseFailMode("drop"); !ok || mode != failModeDrop {
		t.Fatal("expected drop mode")
	}
	if mode, ok := parseFailMode("block"); !ok || mode != failModeBlock {
		t.Fatal("expected block mode")
	}
	if mode, ok := parseFailMode("disconnect"); !ok || mode != failModeDisconnect {
		t.Fatal("expected disconnect mode")
	}
	if mode, ok := parseFailMode("unknown"); ok || mode != failModeDrop {
		t.Fatal("expected default drop on invalid mode")
	}
}

func TestQueueMessageJSONIncludesUserProps(t *testing.T) {
	msg := queueMessage{
		QueueTS:    "2026-01-24T04:00:19Z",
		Topic:      "test/123",
		PayloadB64: "aGVsbG8=",
		QoS:        1,
		Retain:     true,
		UserProperties: []userProperty{
			{Key: "rr", Value: "bbb"},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	if !strings.Contains(string(data), "\"user_properties\"") {
		t.Fatalf("expected user_properties in JSON, got %s", string(data))
	}
}
