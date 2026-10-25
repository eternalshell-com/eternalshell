// Package report builds the periodic status report: what needs attention, what
// changed since the last report, and the numbers behind it. The schedule and the
// comparison of two states are pure; the rest gathers data and writes text.
package report

import (
	"fmt"
	"strings"
	"time"

	"eternalshell/internal/config"
)

// Schedule says when the report is due: every day, or once a week, at a time of
// day in the server's local time.
type Schedule struct {
	Weekly  bool
	Weekday time.Weekday // only used when Weekly
	Hour    int
	Minute  int
	Loc     *time.Location // nil means the local time zone
}

// ScheduleFrom builds a Schedule from the config (already validated).
func ScheduleFrom(r config.Report) (Schedule, error) {
	hour, minute, err := config.ParseClock(r.Time)
	if err != nil {
		return Schedule{}, fmt.Errorf("alerts.report.time: %w", err)
	}
	s := Schedule{Hour: hour, Minute: minute, Loc: time.Local}
	switch strings.ToLower(strings.TrimSpace(r.Schedule)) {
	case "daily":
	case "weekly":
		s.Weekly = true
		if s.Weekday, err = config.ParseWeekday(r.Weekday); err != nil {
			return Schedule{}, fmt.Errorf("alerts.report.weekday: %w", err)
		}
	default:
		return Schedule{}, fmt.Errorf("alerts.report.schedule must be daily or weekly, got %q", r.Schedule)
	}
	return s, nil
}

func (s Schedule) location() *time.Location {
	if s.Loc == nil {
		return time.Local
	}
	return s.Loc
}

// at is the scheduled time of day on the calendar day of t. It is built with
// time.Date for that day, so a daylight saving change moves it correctly.
func (s Schedule) at(t time.Time) time.Time {
	d := t.In(s.location())
	return time.Date(d.Year(), d.Month(), d.Day(), s.Hour, s.Minute, 0, 0, s.location())
}

// matches reports whether the schedule runs on the weekday of t.
func (s Schedule) matches(t time.Time) bool {
	return !s.Weekly || t.In(s.location()).Weekday() == s.Weekday
}

// LastDue is the latest scheduled time that is not after now.
func (s Schedule) LastDue(now time.Time) time.Time {
	now = now.In(s.location())
	for i := 0; i <= 7; i++ {
		day := now.AddDate(0, 0, -i)
		if !s.matches(day) {
			continue
		}
		if t := s.at(day); !t.After(now) {
			return t
		}
	}
	return time.Time{}
}

// NextRun is the earliest scheduled time after now.
func (s Schedule) NextRun(now time.Time) time.Time {
	now = now.In(s.location())
	for i := 0; i <= 7; i++ {
		day := now.AddDate(0, 0, i)
		if !s.matches(day) {
			continue
		}
		if t := s.at(day); t.After(now) {
			return t
		}
	}
	return time.Time{}
}

// Due reports whether a report has to be sent now: the last scheduled time has
// passed and no report was made since. This also catches a missed time (the
// watcher was down at 08:00 and starts at 09:30) and never fires twice for the
// same time. Without a previous report (lastReport is zero) nothing is due; the
// first report then comes at the next scheduled time.
func (s Schedule) Due(lastReport, now time.Time) bool {
	return !lastReport.IsZero() && lastReport.Before(s.LastDue(now))
}

// String describes the schedule, e.g. "daily at 08:00".
func (s Schedule) String() string {
	clock := fmt.Sprintf("%02d:%02d", s.Hour, s.Minute)
	if s.Weekly {
		return fmt.Sprintf("weekly on %s at %s", strings.ToLower(s.Weekday.String()), clock)
	}
	return "daily at " + clock
}
