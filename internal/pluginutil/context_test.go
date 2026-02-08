package pluginutil

import (
	"context"
	"testing"
	"time"
)

func TestTimeoutContext(t *testing.T) {
	ctx, cancel := TimeoutContext(100 * time.Millisecond)
	defer cancel()
	if deadline, ok := ctx.Deadline(); !ok {
		t.Fatal("TimeoutContext expected deadline to be set")
	} else if remaining := time.Until(deadline); remaining < 40*time.Millisecond || remaining > 120*time.Millisecond {
		t.Fatalf("TimeoutContext deadline remaining %v outside expected range", remaining)
	}

	ctx, cancel = TimeoutContext(0)
	cancel()
	if ctx != context.Background() {
		t.Fatalf("TimeoutContext with timeout<=0 should return Background context")
	}
}
