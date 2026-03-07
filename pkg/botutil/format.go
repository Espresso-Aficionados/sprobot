package botutil

import (
	"fmt"
	"time"
)

// FormatAge formats a duration as a human-readable age string
// (e.g. "3 days", "1 year 42 days", "15 mins").
func FormatAge(d time.Duration) string {
	switch {
	case d >= 365*24*time.Hour:
		days := int(d.Hours() / 24)
		years := days / 365
		rem := days % 365
		if rem == 0 {
			if years == 1 {
				return "1 year"
			}
			return fmt.Sprintf("%d years", years)
		}
		dayWord := "days"
		if rem == 1 {
			dayWord = "day"
		}
		if years == 1 {
			return fmt.Sprintf("1 year %d %s", rem, dayWord)
		}
		return fmt.Sprintf("%d years %d %s", years, rem, dayWord)
	case d >= 24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	case d >= time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		mins := int(d.Minutes())
		if mins <= 1 {
			return "1 min"
		}
		return fmt.Sprintf("%d mins", mins)
	}
}
