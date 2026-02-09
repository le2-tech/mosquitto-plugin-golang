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

func TestShouldSampleEveryOne(t *testing.T) {
	t.Parallel()

	var c uint64
	for i := 0; i < 5; i++ {
		if !ShouldSample(&c, 1) {
			t.Fatalf("ShouldSample every=1 should always be true at i=%d", i)
		}
	}
}

func TestShouldSampleModulo(t *testing.T) {
	t.Parallel()

	var c uint64
	wantTrueAt := map[int]struct{}{4: {}, 8: {}, 12: {}}
	for i := 1; i <= 12; i++ {
		got := ShouldSample(&c, 4)
		_, want := wantTrueAt[i]
		if got != want {
			t.Fatalf("ShouldSample step=%d got=%v want=%v", i, got, want)
		}
	}
}
