package idleloop

import "time"

// Config holds the parameters for the idle-repost select loop.
type Config struct {
	MinIdleMins       int
	MaxIdleMins       int
	MsgThreshold      int
	TimeThresholdMins int
	LastPostTime      time.Time
}

// Run blocks, driving the idle-repost select loop.
// It reads from msgCh/stopCh and calls repost() when thresholds are met.
//
// The idle trigger is "armed" by either MsgThreshold messages arriving or
// TimeThresholdMins elapsing (whichever comes first). Once armed, the idle
// timer (MinIdleMins) resets on each new message. When the idle timer fires
// (no messages for MinIdleMins), repost is called. MaxIdleMins starts
// counting from the moment of arming and forces a repost after that duration.
func Run(cfg Config, msgCh <-chan struct{}, stopCh <-chan struct{}, repost func() bool) {
	var msgsSinceLast int
	var idleArmed bool

	maxIdle := time.Duration(cfg.MaxIdleMins) * time.Minute
	minIdle := time.Duration(cfg.MinIdleMins) * time.Minute

	var timeThreshTimer *time.Timer
	if cfg.TimeThresholdMins > 0 {
		initialTimeThresh := time.Duration(cfg.TimeThresholdMins)*time.Minute - time.Since(cfg.LastPostTime)
		if initialTimeThresh <= 0 || cfg.LastPostTime.IsZero() {
			initialTimeThresh = time.Second
		}
		timeThreshTimer = time.NewTimer(initialTimeThresh)
	}

	var idleTimer *time.Timer
	var maxTimer *time.Timer

	defer func() {
		if maxTimer != nil {
			maxTimer.Stop()
		}
		if timeThreshTimer != nil {
			timeThreshTimer.Stop()
		}
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	timeThreshC := func() <-chan time.Time {
		if timeThreshTimer == nil {
			return nil
		}
		return timeThreshTimer.C
	}

	idleTimerC := func() <-chan time.Time {
		if idleTimer == nil {
			return nil
		}
		return idleTimer.C
	}

	maxTimerC := func() <-chan time.Time {
		if maxTimer == nil {
			return nil
		}
		return maxTimer.C
	}

	arm := func() {
		if idleArmed {
			return
		}
		idleArmed = true
		if idleTimer == nil {
			idleTimer = time.NewTimer(minIdle)
		} else {
			idleTimer.Reset(minIdle)
		}
		if maxTimer == nil {
			maxTimer = time.NewTimer(maxIdle)
		} else {
			maxTimer.Reset(maxIdle)
		}
	}

	resetAll := func() {
		msgsSinceLast = 0
		idleArmed = false
		if maxTimer != nil {
			maxTimer.Stop()
			maxTimer = nil
		}
		if timeThreshTimer != nil {
			if !timeThreshTimer.Stop() {
				select {
				case <-timeThreshTimer.C:
				default:
				}
			}
			timeThreshTimer.Reset(time.Duration(cfg.TimeThresholdMins) * time.Minute)
		}
		if idleTimer != nil {
			idleTimer.Stop()
			idleTimer = nil
		}
	}

	for {
		select {
		case <-stopCh:
			return

		case <-msgCh:
			msgsSinceLast++
			if idleArmed {
				if idleTimer != nil {
					if !idleTimer.Stop() {
						select {
						case <-idleTimer.C:
						default:
						}
					}
					idleTimer.Reset(minIdle)
				}
			} else if cfg.MsgThreshold > 0 && msgsSinceLast >= cfg.MsgThreshold {
				arm()
			}

		case <-timeThreshC():
			if msgsSinceLast >= 1 {
				arm()
			}

		case <-idleTimerC():
			if repost() {
				resetAll()
			} else {
				idleTimer = nil
			}

		case <-maxTimerC():
			if msgsSinceLast >= 1 {
				if repost() {
					resetAll()
				} else {
					maxTimer.Reset(maxIdle)
				}
			} else {
				maxTimer.Reset(maxIdle)
			}
		}
	}
}
