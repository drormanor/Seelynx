// Package schedule decides whether a weekly schedule is active at a given time.
package schedule

import (
	"errors"
	"fmt"
	"time"
)

// Period is a daily time window on the given weekdays (0 is Sunday). Times are
// "HH:MM" in server local time. An End earlier than Start runs past midnight, and
// equal times mean the whole day.
type Period struct {
	Days  []int  `json:"days"`
	Start string `json:"start"`
	End   string `json:"end"`
}

func minutes(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("invalid time %q", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Validate reports the first problem in a list of periods.
func Validate(ps []Period) error {
	if len(ps) == 0 {
		return errors.New("Add at least one time period.")
	}
	for _, p := range ps {
		if len(p.Days) == 0 {
			return errors.New("Choose at least one day for each period.")
		}
		for _, d := range p.Days {
			if d < 0 || d > 6 {
				return errors.New("Invalid day.")
			}
		}
		if _, err := minutes(p.Start); err != nil {
			return errors.New("Enter times as HH:MM.")
		}
		if _, err := minutes(p.End); err != nil {
			return errors.New("Enter times as HH:MM.")
		}
	}
	return nil
}

func onDay(p Period, day int) bool {
	for _, d := range p.Days {
		if d == day {
			return true
		}
	}
	return false
}

// Active reports whether any period covers t.
func Active(ps []Period, t time.Time) bool {
	now := t.Hour()*60 + t.Minute()
	today := int(t.Weekday())
	yesterday := (today + 6) % 7
	for _, p := range ps {
		start, err1 := minutes(p.Start)
		end, err2 := minutes(p.End)
		if err1 != nil || err2 != nil {
			continue
		}
		switch {
		case start == end:
			if onDay(p, today) {
				return true
			}
		case start < end:
			if onDay(p, today) && now >= start && now < end {
				return true
			}
		default: // overnight: the tail belongs to the day it started on
			if onDay(p, today) && now >= start {
				return true
			}
			if onDay(p, yesterday) && now < end {
				return true
			}
		}
	}
	return false
}
