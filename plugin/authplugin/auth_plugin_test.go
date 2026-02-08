package main

import (
	"context"
	"testing"
	"time"

	"mosquitto-plugin/internal/pluginutil"
)

func TestCtxTimeout(t *testing.T) {
	ctx, cancel := pluginutil.TimeoutContext(100 * time.Millisecond)
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok {
		t.Fatal("ctxTimeout expected deadline to be set")
	} else if remaining := time.Until(deadline); remaining < 40*time.Millisecond || remaining > 120*time.Millisecond {
		t.Fatalf("ctxTimeout deadline remaining %v outside expected range", remaining)
	}

	ctx, cancel = pluginutil.TimeoutContext(0)
	cancel()
	if ctx != context.Background() {
		t.Fatalf("ctxTimeout with timeout<=0 should return Background context")
	}
}
