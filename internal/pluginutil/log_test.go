package pluginutil

import "testing"

func TestFormatLogMessage(t *testing.T) {
	t.Parallel()

	got := FormatLogMessage("msg", map[string]any{"b": 2, "a": 1})
	want := "msg a=1 b=2"
	if got != want {
		t.Fatalf("FormatLogMessage() = %q, want %q", got, want)
	}
}

func TestFormatLogMessageNoFields(t *testing.T) {
	t.Parallel()

	if got := FormatLogMessage("msg"); got != "msg" {
		t.Fatalf("FormatLogMessage without fields = %q, want %q", got, "msg")
	}
	if got := FormatLogMessage("msg", nil); got != "msg" {
		t.Fatalf("FormatLogMessage with nil fields = %q, want %q", got, "msg")
	}
}
