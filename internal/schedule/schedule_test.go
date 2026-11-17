package schedule

import (
	"testing"
	"time"
)

// 2026-11-02 is a Monday.
func at(day int, hhmm string) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(2026, 11, 1+day, t.Hour(), t.Minute(), 0, 0, time.Local)
}

func TestActive(t *testing.T) {
	weekdays := []Period{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "18:00"}}
	night := []Period{{Days: []int{5}, Start: "22:00", End: "06:00"}} // Friday night
	allDay := []Period{{Days: []int{0}, Start: "00:00", End: "00:00"}}
	cases := []struct {
		name string
		ps   []Period
		t    time.Time
		want bool
	}{
		{"inside", weekdays, at(1, "12:00"), true},
		{"at start", weekdays, at(1, "08:00"), true},
		{"at end is excluded", weekdays, at(1, "18:00"), false},
		{"before", weekdays, at(1, "07:59"), false},
		{"wrong day", weekdays, at(0, "12:00"), false},
		{"overnight evening", night, at(5, "23:00"), true},
		{"overnight next morning", night, at(6, "05:59"), true},
		{"overnight ends", night, at(6, "06:00"), false},
		{"overnight not the morning it starts", night, at(5, "05:00"), false},
		{"whole day", allDay, at(0, "13:37"), true},
		{"whole day other day", allDay, at(1, "13:37"), false},
		{"saturday night wraps to sunday", []Period{{Days: []int{6}, Start: "22:00", End: "02:00"}}, at(7, "01:00"), true},
	}
	for _, c := range cases {
		if got := Active(c.ps, c.t); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestValidate(t *testing.T) {
	good := []Period{{Days: []int{1}, Start: "08:00", End: "09:00"}}
	if err := Validate(good); err != nil {
		t.Fatal(err)
	}
	bad := [][]Period{
		nil,
		{{Start: "08:00", End: "09:00"}},
		{{Days: []int{7}, Start: "08:00", End: "09:00"}},
		{{Days: []int{1}, Start: "8am", End: "09:00"}},
		{{Days: []int{1}, Start: "08:00", End: "25:00"}},
	}
	for i, ps := range bad {
		if Validate(ps) == nil {
			t.Errorf("case %d should fail", i)
		}
	}
}
