package botutil

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestRunSaveLoopCallsAfterReady(t *testing.T) {
	var ready atomic.Bool
	stop := make(chan struct{})
	called := make(chan struct{}, 1)

	ready.Store(true)

	go RunSaveLoop(&ready, 10*time.Millisecond, stop, func() {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	select {
	case <-called:
		// success
	case <-time.After(2 * time.Second):
		t.Error("fn was not called within timeout")
	}
	close(stop)
}

func TestRunSaveLoopRespectsStop(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		RunSaveLoop(&ready, 1*time.Hour, stop, func() {})
		close(done)
	}()

	close(stop)
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Error("RunSaveLoop did not exit after stop")
	}
}

func TestRunSaveLoopStopsBeforeReady(t *testing.T) {
	var ready atomic.Bool
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		RunSaveLoop(&ready, 1*time.Hour, stop, func() {
			t.Error("fn should not be called before ready")
		})
		close(done)
	}()

	close(stop)
	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Error("RunSaveLoop did not exit after stop while waiting for ready")
	}
}

func TestRunSaveLoopWaitsForReady(t *testing.T) {
	var ready atomic.Bool
	stop := make(chan struct{})
	called := make(chan struct{}, 1)

	go RunSaveLoop(&ready, 10*time.Millisecond, stop, func() {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// fn should not be called yet
	select {
	case <-called:
		t.Error("fn called before ready")
	case <-time.After(50 * time.Millisecond):
	}

	// Now set ready
	ready.Store(true)

	select {
	case <-called:
		// success
	case <-time.After(2 * time.Second):
		t.Error("fn was not called after ready was set")
	}
	close(stop)
}
