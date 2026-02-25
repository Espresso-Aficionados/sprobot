package botutil

import (
	"log/slog"
	"sync/atomic"
	"time"
)

// RunSaveLoop waits for ready to become true, then calls fn on each tick of interval.
// It returns when stop is closed. Panics in fn are recovered so the loop keeps running.
func RunSaveLoop(ready *atomic.Bool, interval time.Duration, stop <-chan struct{}, fn func()) {
	readyTicker := time.NewTicker(1 * time.Second)
	defer readyTicker.Stop()
	for !ready.Load() {
		select {
		case <-stop:
			return
		case <-readyTicker.C:
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			safeCall(fn)
		}
	}
}

func safeCall(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Panic in save loop", "error", r)
		}
	}()
	fn()
}
