package requiredset

import (
	"errors"
	"strings"
	"time"
)

// ParseQuarantineExpiry reads an RFC 3339 time or a duration ("36h", "7d").
func ParseQuarantineExpiry(until, duration string, now time.Time) (time.Time, error) {
	until, duration = strings.TrimSpace(until), strings.TrimSpace(duration)
	switch {
	case until != "" && duration != "":
		return time.Time{}, errors.New("give until or duration, not both")
	case until != "":
		value, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return time.Time{}, errors.New("until must be an RFC 3339 time")
		}
		return value.UTC(), nil
	case duration != "":
		if days, ok := strings.CutSuffix(duration, "d"); ok {
			value, err := time.ParseDuration(days + "h")
			if err != nil {
				return time.Time{}, errors.New("duration must be like 72h or 7d")
			}
			return now.Add(value * 24).UTC(), nil
		}
		value, err := time.ParseDuration(duration)
		if err != nil {
			return time.Time{}, errors.New("duration must be like 72h or 7d")
		}
		return now.Add(value).UTC(), nil
	}
	return time.Time{}, errors.New("a quarantine needs an expiry: until or duration")
}

// ParseExpiryText reads one text: an RFC 3339 time, or a duration ("72h",
// "7d").
func ParseExpiryText(value string, now time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if _, err := time.Parse(time.RFC3339, value); err == nil {
		return ParseQuarantineExpiry(value, "", now)
	}
	return ParseQuarantineExpiry("", value, now)
}
