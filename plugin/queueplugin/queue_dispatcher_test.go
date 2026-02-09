package main

import (
	"errors"
	"testing"
	"time"
)

func withDispatcherTestSetup(t *testing.T, fn func(release chan struct{})) {
	t.Helper()

	oldCfg := cfg
	oldPublish := dispatchPublishFn
	release := make(chan struct{})

	cfg.enqueueTimeout = 20 * time.Millisecond
	cfg.publishTimeout = 20 * time.Millisecond
	dispatchPublishFn = func([]byte) { <-release }
	stopDispatcher()
	startDispatcher(1)

	t.Cleanup(func() {
		close(release)
		stopDispatcher()
		dispatchPublishFn = oldPublish
		cfg = oldCfg
	})

	fn(release)
}

func fillQueueUntilFull(t *testing.T) {
	t.Helper()
	for i := 0; i < 256; i++ {
		err := enqueueMessage([]byte("x"))
		if errors.Is(err, errQueueFull) || errors.Is(err, errEnqueueTimeout) {
			return
		}
	}
	t.Fatal("queue did not become full in expected iterations")
}

func TestEnqueueMessageDropWhenFull(t *testing.T) {
	withDispatcherTestSetup(t, func(release chan struct{}) {
		cfg.failMode = failModeDrop
		fillQueueUntilFull(t)

		err := enqueueMessage([]byte("x"))
		if !errors.Is(err, errQueueFull) {
			t.Fatalf("enqueueMessage(drop) got err=%v want=%v", err, errQueueFull)
		}
	})
}

func TestEnqueueMessageDisconnectWhenFull(t *testing.T) {
	withDispatcherTestSetup(t, func(release chan struct{}) {
		cfg.failMode = failModeDisconnect
		fillQueueUntilFull(t)

		err := enqueueMessage([]byte("x"))
		if !errors.Is(err, errQueueFull) {
			t.Fatalf("enqueueMessage(disconnect) got err=%v want=%v", err, errQueueFull)
		}
	})
}

func TestEnqueueMessageBlockTimeout(t *testing.T) {
	withDispatcherTestSetup(t, func(release chan struct{}) {
		cfg.failMode = failModeBlock
		cfg.enqueueTimeout = 10 * time.Millisecond
		fillQueueUntilFull(t)

		start := time.Now()
		err := enqueueMessage([]byte("x"))
		if !errors.Is(err, errEnqueueTimeout) {
			t.Fatalf("enqueueMessage(block) got err=%v want=%v", err, errEnqueueTimeout)
		}
		if elapsed := time.Since(start); elapsed < 8*time.Millisecond {
			t.Fatalf("enqueueMessage(block) timeout too short: %v", elapsed)
		}
	})
}

func TestEnqueueMessageStopped(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() { cfg = oldCfg })

	stopDispatcher()
	cfg.failMode = failModeDrop
	cfg.enqueueTimeout = 10 * time.Millisecond

	err := enqueueMessage([]byte("x"))
	if !errors.Is(err, errDispatcherStopped) {
		t.Fatalf("enqueueMessage(stopped) got err=%v want=%v", err, errDispatcherStopped)
	}
}

func TestStopDispatcherBoundedWhenPublisherBlocked(t *testing.T) {
	oldCfg := cfg
	oldPublish := dispatchPublishFn
	oldStopWait := dispatcherStopWait
	oldStopTimeoutLogFn := dispatcherStopTimeoutLogFn
	release := make(chan struct{})
	started := make(chan struct{})
	timeoutLogged := false

	cfg.enqueueTimeout = 10 * time.Millisecond
	dispatchPublishFn = func([]byte) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
	}
	dispatcherStopWait = 15 * time.Millisecond
	dispatcherStopTimeoutLogFn = func(time.Duration, int) {
		timeoutLogged = true
	}

	t.Cleanup(func() {
		close(release)
		stopDispatcher()
		dispatchPublishFn = oldPublish
		dispatcherStopWait = oldStopWait
		dispatcherStopTimeoutLogFn = oldStopTimeoutLogFn
		cfg = oldCfg
	})

	stopDispatcher()
	startDispatcher(1)
	if err := enqueueMessage([]byte("x")); err != nil {
		t.Fatalf("enqueueMessage failed: %v", err)
	}
	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("dispatcher did not start publish in time")
	}

	start := time.Now()
	stopDispatcher()
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("stopDispatcher took too long: %v", elapsed)
	}
	if !timeoutLogged {
		t.Fatal("expected timeout warning hook to be called")
	}
}
