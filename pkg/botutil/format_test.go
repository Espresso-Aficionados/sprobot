package botutil

import (
	"testing"
	"time"
)

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"one minute", time.Minute, "1 min"},
		{"zero", 0, "1 min"},
		{"30 seconds", 30 * time.Second, "1 min"},
		{"5 minutes", 5 * time.Minute, "5 mins"},
		{"1 hour", time.Hour, "1 hour"},
		{"3 hours", 3 * time.Hour, "3 hours"},
		{"1 day", 24 * time.Hour, "1 day"},
		{"7 days", 7 * 24 * time.Hour, "7 days"},
		{"1.5 days", 36 * time.Hour, "1 day"},
		{"364 days", 364 * 24 * time.Hour, "364 days"},
		{"exactly 1 year", 365 * 24 * time.Hour, "1 year"},
		{"1 year 1 day", 366 * 24 * time.Hour, "1 year 1 day"},
		{"2 years", 730 * 24 * time.Hour, "2 years"},
		{"4 years 135 days", (4*365 + 135) * 24 * time.Hour, "4 years 135 days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAge(tt.duration)
			if got != tt.expected {
				t.Errorf("FormatAge(%v) = %q, want %q", tt.duration, got, tt.expected)
			}
		})
	}
}
