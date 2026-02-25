package stickybot

import "testing"

func TestValidateStickyParams(t *testing.T) {
	tests := []struct {
		name                                               string
		minIdle, maxIdle, threshold, timeThreshold, buffer int
		wantOK                                             bool
	}{
		{"valid defaults", 15, 30, 30, 10, 5, true},
		{"valid no buffer", 0, 10, 5, 0, 0, true},
		{"valid time only", 0, 10, 0, 5, 0, true},
		{"max_idle equals min_idle", 15, 15, 10, 0, 0, false},
		{"max_idle less than min_idle", 30, 15, 10, 0, 0, false},
		{"threshold equals buffer", 0, 10, 5, 0, 5, false},
		{"threshold less than buffer", 0, 10, 3, 0, 5, false},
		{"both thresholds zero", 0, 10, 0, 0, 0, false},
		{"threshold zero buffer nonzero ok", 0, 10, 0, 5, 3, true},
		{"min_idle zero valid", 0, 1, 1, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errMsg := validateStickyParams(tt.minIdle, tt.maxIdle, tt.threshold, tt.timeThreshold, tt.buffer)
			if tt.wantOK && errMsg != "" {
				t.Errorf("expected OK, got error: %q", errMsg)
			}
			if !tt.wantOK && errMsg == "" {
				t.Error("expected error, got OK")
			}
		})
	}
}
