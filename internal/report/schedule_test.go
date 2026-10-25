package report

import (
	"testing"
	"time"
)

var berlin = time.FixedZone("CET", 3600) // a fixed zone that is not UTC

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, berlin)
}

func daily(h, m int) Schedule { return Schedule{Hour: h, Minute: m, Loc: berlin} }

func weekly(day time.Weekday, h, m int) Schedule {
	return Schedule{Weekly: true, Weekday: day, Hour: h, Minute: m, Loc: berlin}
}

func TestDailyLastDueAndNextRun(t *testing.T) {
	s := daily(8, 0)
	tests := []struct {
		name    string
		now     time.Time
		lastDue time.Time
		nextRun time.Time
	}{
		{"before the time", at(2026, 10, 25, 7, 59), at(2026, 10, 24, 8, 0), at(2026, 10, 25, 8, 0)},
		{"exactly at the time", at(2026, 10, 25, 8, 0), at(2026, 10, 25, 8, 0), at(2026, 10, 26, 8, 0)},
		{"after the time", at(2026, 10, 25, 14, 30), at(2026, 10, 25, 8, 0), at(2026, 10, 26, 8, 0)},
		{"over a month end", at(2026, 11, 1, 3, 0), at(2026, 10, 31, 8, 0), at(2026, 11, 1, 8, 0)},
	}
	for _, tt := range tests {
		if got := s.LastDue(tt.now); !got.Equal(tt.lastDue) {
			t.Errorf("%s: LastDue = %v, want %v", tt.name, got, tt.lastDue)
		}
		if got := s.NextRun(tt.now); !got.Equal(tt.nextRun) {
			t.Errorf("%s: NextRun = %v, want %v", tt.name, got, tt.nextRun)
		}
	}
}

func TestScheduleUsesItsOwnZone(t *testing.T) {
	// 07:30 UTC is 08:30 in the schedule's zone (UTC+1), so 08:00 has passed there
	s := daily(8, 0)
	now := time.Date(2026, 10, 25, 7, 30, 0, 0, time.UTC)
	if got := s.LastDue(now); !got.Equal(at(2026, 10, 25, 8, 0)) {
		t.Errorf("LastDue = %v", got)
	}
}

func TestWeeklyLastDueAndNextRun(t *testing.T) {
	// 26 Oct 2026 is a Monday
	s := weekly(time.Monday, 8, 0)
	tests := []struct {
		name    string
		now     time.Time
		lastDue time.Time
		nextRun time.Time
	}{
		{"day before", at(2026, 10, 25, 20, 0), at(2026, 10, 19, 8, 0), at(2026, 10, 26, 8, 0)},
		{"the day, before the time", at(2026, 10, 26, 7, 0), at(2026, 10, 19, 8, 0), at(2026, 10, 26, 8, 0)},
		{"the day, after the time", at(2026, 10, 26, 9, 0), at(2026, 10, 26, 8, 0), at(2026, 11, 2, 8, 0)},
		{"later in the week", at(2026, 10, 30, 12, 0), at(2026, 10, 26, 8, 0), at(2026, 11, 2, 8, 0)},
	}
	for _, tt := range tests {
		if got := s.LastDue(tt.now); !got.Equal(tt.lastDue) {
			t.Errorf("%s: LastDue = %v, want %v", tt.name, got, tt.lastDue)
		}
		if got := s.NextRun(tt.now); !got.Equal(tt.nextRun) {
			t.Errorf("%s: NextRun = %v, want %v", tt.name, got, tt.nextRun)
		}
	}
}

func TestDue(t *testing.T) {
	s := daily(8, 0)
	now := at(2026, 10, 25, 9, 30)

	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"missed: the last report is from yesterday", at(2026, 10, 24, 8, 1), true},
		{"missed for several days", at(2026, 10, 20, 8, 1), true},
		{"sent at the scheduled time today", at(2026, 10, 25, 8, 0), false},
		{"sent a little after the scheduled time", at(2026, 10, 25, 8, 2), false},
		{"sent by hand this morning", at(2026, 10, 25, 9, 0), false},
		{"no previous report: nothing is due", time.Time{}, false},
	}
	for _, tt := range tests {
		if got := s.Due(tt.last, now); got != tt.want {
			t.Errorf("%s: Due = %v, want %v", tt.name, got, tt.want)
		}
	}

	// never twice for the same time: right after a send it is not due, and it
	// becomes due again exactly at the next scheduled time
	sent := at(2026, 10, 25, 9, 30)
	if s.Due(sent, at(2026, 10, 25, 9, 31)) || s.Due(sent, at(2026, 10, 26, 7, 59)) {
		t.Error("must not be due again before the next scheduled time")
	}
	if !s.Due(sent, at(2026, 10, 26, 8, 0)) {
		t.Error("must be due at the next scheduled time")
	}

	// weekly: a report from last Monday is not due on Wednesday before the next Monday
	w := weekly(time.Monday, 8, 0)
	if w.Due(at(2026, 10, 26, 8, 1), at(2026, 10, 28, 12, 0)) {
		t.Error("weekly: not due in the middle of the week")
	}
	if !w.Due(at(2026, 10, 26, 8, 1), at(2026, 11, 2, 8, 0)) {
		t.Error("weekly: due on the next Monday at the scheduled time")
	}
}

func TestScheduleString(t *testing.T) {
	if got := daily(8, 5).String(); got != "daily at 08:05" {
		t.Errorf("got %q", got)
	}
	if got := weekly(time.Friday, 18, 30).String(); got != "weekly on friday at 18:30" {
		t.Errorf("got %q", got)
	}
}
