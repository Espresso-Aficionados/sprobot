package botutil

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sadbox/svc/telemetry"
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

// RunJobLoop is RunSaveLoop with each call of fn a run of job: fn's error
// is the run's outcome. fn logs its own failures; the loop doesn't repeat
// them.
func RunJobLoop(ready *atomic.Bool, interval time.Duration, stop <-chan struct{}, job *telemetry.Job, fn func(context.Context) error) {
	RunSaveLoop(ready, interval, stop, func() {
		// The panic job.Run re-raises is safeCall's.
		_ = job.Run(context.Background(), fn)
	})
}
