package botutil

import (
	"sync/atomic"
	"time"
)

// RunSaveLoop waits for ready to become true, then calls fn on each tick of interval.
func RunSaveLoop(ready *atomic.Bool, interval time.Duration, fn func()) {
	for !ready.Load() {
		time.Sleep(1 * time.Second)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		fn()
	}
}
