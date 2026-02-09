package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"mosquitto-plugin/internal/pluginutil"
)

var (
	dispatchMu   sync.RWMutex
	dispatchCh   chan []byte
	dispatchStop chan struct{}
	dispatchDone chan struct{}
)

var dispatchPublishFn = publishQueued
var dispatcherStopWait = 3 * time.Second
var dispatcherStopTimeoutLogFn = func(wait time.Duration, pending int) {
	log(mosqLogWarning, "queue-plugin: dispatcher stop timeout", map[string]any{
		"wait_ms": int(wait / time.Millisecond),
		"pending": pending,
	})
}

var (
	errDispatcherStopped = errors.New("queue-plugin: dispatcher stopped")
	errQueueFull         = errors.New("queue-plugin: queue full")
	errEnqueueTimeout    = errors.New("queue-plugin: enqueue timeout")
)

func startDispatcher(buffer int) {
	stopDispatcher()

	ch := make(chan []byte, buffer)
	stop := make(chan struct{})
	done := make(chan struct{})

	dispatchMu.Lock()
	dispatchCh = ch
	dispatchStop = stop
	dispatchDone = done
	dispatchMu.Unlock()

	go dispatchWorker(ch, stop, done)
}

func stopDispatcher() {
	dispatchMu.Lock()
	stop := dispatchStop
	done := dispatchDone
	pending := 0
	if dispatchCh != nil {
		pending = len(dispatchCh)
	}
	dispatchStop = nil
	dispatchCh = nil
	dispatchDone = nil
	dispatchMu.Unlock()

	if stop == nil {
		return
	}

	close(stop)
	timer := time.NewTimer(dispatcherStopWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		dispatcherStopTimeoutLogFn(dispatcherStopWait, pending)
	}
}

func dispatchWorker(ch <-chan []byte, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	for {
		select {
		case <-stop:
			return
		default:
		}

		select {
		case body := <-ch:
			dispatchPublishFn(body)
		case <-stop:
			return
		}
	}
}

func publishQueued(body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.publishTimeout)
	defer cancel()
	if err := publisher.Publish(ctx, body); err != nil {
		if pluginutil.ShouldSample(&workerWarnCounter, debugSampleEvery) {
			log(mosqLogWarning, "queue-plugin worker publish failed", map[string]any{"error": err})
		}
	}
}

func enqueueMessage(body []byte) error {
	dispatchMu.RLock()
	ch := dispatchCh
	stop := dispatchStop
	mode := cfg.failMode
	wait := cfg.enqueueTimeout
	dispatchMu.RUnlock()

	if ch == nil || stop == nil {
		return errDispatcherStopped
	}

	select {
	case <-stop:
		return errDispatcherStopped
	default:
	}

	switch mode {
	case failModeDrop, failModeDisconnect:
		select {
		case ch <- body:
			return nil
		case <-stop:
			return errDispatcherStopped
		default:
			return errQueueFull
		}
	case failModeBlock:
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case ch <- body:
			return nil
		case <-stop:
			return errDispatcherStopped
		case <-timer.C:
			return errEnqueueTimeout
		}
	default:
		return errQueueFull
	}
}
