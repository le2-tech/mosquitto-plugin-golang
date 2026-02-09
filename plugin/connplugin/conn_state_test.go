package main

import (
	"errors"
	"testing"
)

func resetConnState() {
	activeConnMu.Lock()
	activeConn = map[uintptr]struct{}{}
	activeConnMu.Unlock()
	debugSkipCounter = 0
	debugRecordCounter = 0
}

func TestHandleDisconnectByKeyAlwaysClearsState(t *testing.T) {
	oldDebugLogger := debugLogger
	oldWarnLogger := warnLogger
	t.Cleanup(func() {
		debugLogger = oldDebugLogger
		warnLogger = oldWarnLogger
	})
	debugLogger = func(string, map[string]any) {}
	warnLogger = func(string, map[string]any) {}

	resetConnState()
	key := uintptr(12345)
	setConnectedByKey(key, true)

	called := false
	handleDisconnectByKey(key, func() error {
		called = true
		return errors.New("write failed")
	})

	if !called {
		t.Fatal("record callback should be called")
	}
	if connectedByKey(key) {
		t.Fatal("connection state should be cleared even if record fails")
	}
}

func TestHandleDisconnectByKeySkipWhenNotConnected(t *testing.T) {
	oldDebugLogger := debugLogger
	oldWarnLogger := warnLogger
	t.Cleanup(func() {
		debugLogger = oldDebugLogger
		warnLogger = oldWarnLogger
	})
	debugLogger = func(string, map[string]any) {}
	warnLogger = func(string, map[string]any) {}

	resetConnState()
	key := uintptr(999)

	called := false
	handleDisconnectByKey(key, func() error {
		called = true
		return nil
	})

	if called {
		t.Fatal("record callback should not be called for non-connected key")
	}
}

func TestHandleDisconnectByKeyIdempotent(t *testing.T) {
	oldDebugLogger := debugLogger
	oldWarnLogger := warnLogger
	t.Cleanup(func() {
		debugLogger = oldDebugLogger
		warnLogger = oldWarnLogger
	})
	debugLogger = func(string, map[string]any) {}
	warnLogger = func(string, map[string]any) {}

	resetConnState()
	key := uintptr(123)
	setConnectedByKey(key, true)

	called := 0
	handleDisconnectByKey(key, func() error {
		called++
		return nil
	})
	handleDisconnectByKey(key, func() error {
		called++
		return nil
	})

	if called != 1 {
		t.Fatalf("record callback should be called once, got=%d", called)
	}
}
