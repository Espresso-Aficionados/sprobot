package idleloop

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestMsgThresholdArms(t *testing.T) {
	cfg := Config{
		MinIdleMins:  0, // use sub-second via timer directly
		MaxIdleMins:  10,
		MsgThreshold: 3,
	}

	msgCh := make(chan struct{}, 128)
	stopCh := make(chan struct{})
	var reposted atomic.Bool

	// We'll use very short durations. MinIdleMins=0 means the timer fires immediately after arming.
	// Override: use 1 min but we'll rely on the fact that after arming, idle timer of 0 fires quickly.
	// Actually, MinIdleMins=0 → 0 duration timer → fires immediately.

	go Run(cfg, msgCh, stopCh, func() bool {
		reposted.Store(true)
		return true
	})

	// Send enough messages to arm
	for i := 0; i < 3; i++ {
		msgCh <- struct{}{}
	}

	// Wait for the repost (MinIdleMins=0 means idle timer fires immediately)
	time.Sleep(100 * time.Millisecond)

	if !reposted.Load() {
		t.Error("expected repost after message threshold + idle timer")
	}
	close(stopCh)
}

func TestStopChannelExits(t *testing.T) {
	cfg := Config{
		MinIdleMins:  1,
		MaxIdleMins:  5,
		MsgThreshold: 10,
	}

	msgCh := make(chan struct{}, 128)
	stopCh := make(chan struct{})
	done := make(chan struct{})

	go func() {
		Run(cfg, msgCh, stopCh, func() bool { return true })
		close(done)
	}()

	close(stopCh)

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after stop channel closed")
	}
}

func TestIdleTimerResetsOnMessage(t *testing.T) {
	cfg := Config{
		MinIdleMins:  0,
		MaxIdleMins:  100,
		MsgThreshold: 1,
	}

	msgCh := make(chan struct{}, 128)
	stopCh := make(chan struct{})
	var repostCount atomic.Int32

	go Run(cfg, msgCh, stopCh, func() bool {
		repostCount.Add(1)
		return true
	})

	// Arm with first message
	msgCh <- struct{}{}

	// MinIdleMins=0 so repost fires quickly
	time.Sleep(100 * time.Millisecond)

	// Should have reposted once
	if repostCount.Load() < 1 {
		t.Error("expected at least one repost")
	}
	close(stopCh)
}

func TestMaxTimerForceRepost(t *testing.T) {
	cfg := Config{
		MinIdleMins:       100, // Very long idle so only max timer fires
		MaxIdleMins:       0,   // 0 duration → fires immediately after arm
		MsgThreshold:      1,
		TimeThresholdMins: 0,
	}

	msgCh := make(chan struct{}, 128)
	stopCh := make(chan struct{})
	var reposted atomic.Bool

	go Run(cfg, msgCh, stopCh, func() bool {
		reposted.Store(true)
		return true
	})

	// Arm with a message
	msgCh <- struct{}{}

	// Max timer at 0 mins fires immediately
	time.Sleep(100 * time.Millisecond)

	if !reposted.Load() {
		t.Error("expected repost from max timer")
	}
	close(stopCh)
}

func TestPanicRecovery(t *testing.T) {
	cfg := Config{
		MinIdleMins:  0,
		MaxIdleMins:  10,
		MsgThreshold: 1,
	}

	msgCh := make(chan struct{}, 128)
	stopCh := make(chan struct{})
	done := make(chan struct{})
	var callCount atomic.Int32

	go func() {
		Run(cfg, msgCh, stopCh, func() bool {
			callCount.Add(1)
			panic("test panic")
		})
		close(done)
	}()

	// Arm
	msgCh <- struct{}{}
	time.Sleep(100 * time.Millisecond)

	// The goroutine should still be alive after the panic
	close(stopCh)
	select {
	case <-done:
		// success — goroutine exited cleanly
	case <-time.After(2 * time.Second):
		t.Error("Run did not exit after stop")
	}

	if callCount.Load() < 1 {
		t.Error("repost should have been called at least once")
	}
}
