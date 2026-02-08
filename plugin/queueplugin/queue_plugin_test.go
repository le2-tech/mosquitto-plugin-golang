package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAllowMessage(t *testing.T) {
	if allow, reason := allowMessage("$SYS/broker/uptime"); allow || reason != "sys_topic" {
		t.Fatalf("expected $SYS/# to be filtered, allow=%v reason=%q", allow, reason)
	}
	if allow, reason := allowMessage("$SYS"); allow || reason != "sys_topic" {
		t.Fatalf("expected $SYS to be filtered, allow=%v reason=%q", allow, reason)
	}
	if allow, reason := allowMessage("devices/a/up"); !allow || reason != "" {
		t.Fatalf("expected normal topic to pass, allow=%v reason=%q", allow, reason)
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
		TS:      "2026-01-24T04:00:19Z",
		Topic:   "test/123",
		Payload: "hello",
		QoS:     1,
		Retain:  true,
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
