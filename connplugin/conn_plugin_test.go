package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseBoolOption(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		ok   bool
	}{
		{"1", true, true},
		{"true", true, true},
		{" YES ", true, true},
		{"on", true, true},
		{"0", false, true},
		{"false", false, true},
		{"No", false, true},
		{"off", false, true},
		{"maybe", false, false},
		{"", false, false},
	}

	for _, tc := range cases {
		got, ok := parseBoolOption(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseBoolOption(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseTimeoutMS(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"100", 100 * time.Millisecond, true},
		{" 250 ", 250 * time.Millisecond, true},
		{"0", 0, false},
		{"-1", 0, false},
		{"abc", 0, false},
		{"", 0, false},
	}

	for _, tc := range cases {
		got, ok := parseTimeoutMS(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("parseTimeoutMS(%q) = %v, %v; want %v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSafeDSN(t *testing.T) {
	input := "postgres://user:pass@127.0.0.1:5432/db?sslmode=disable"
	got := safeDSN(input)
	if strings.Contains(got, "pass") {
		t.Fatalf("safeDSN leaked password: %q", got)
	}
	if !strings.Contains(got, "xxxxx") {
		t.Fatalf("safeDSN did not mask password: %q", got)
	}

	noPass := "postgres://user@127.0.0.1:5432/db?sslmode=disable"
	if gotNoPass := safeDSN(noPass); gotNoPass != noPass {
		t.Fatalf("safeDSN changed DSN without password: %q", gotNoPass)
	}

	raw := "not-a-dsn"
	if gotRaw := safeDSN(raw); gotRaw != raw {
		t.Fatalf("safeDSN changed raw string: %q", gotRaw)
	}
}

func TestProtocolString(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{3, "MQTT/3.1"},
		{4, "MQTT/3.1.1"},
		{5, "MQTT/5.0"},
		{0, ""},
	}

	for _, tc := range cases {
		got := protocolString(tc.in)
		if got != tc.want {
			t.Fatalf("protocolString(%d) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
