// Package watch is the watcher: it samples the server, applies the alert rules
// and mails incidents. This file holds the rules. They are pure: no clock, no
// I/O, only numbers in and notices out, so they can be tested without waiting.
package watch

import (
	"sort"
	"time"

	"eternalshell/internal/config"
)

// Interval is the time between two samples.
const Interval = 10 * time.Second

// recentSamples is the length of the "recent" average used for the all-clear:
// 6 samples of 10 s are one minute.
const recentSamples = 6

// Rule names an alert.
type Rule string

const (
	RuleProcess Rule = "cpu-process" // one process above its threshold
	RuleTotal   Rule = "cpu-total"   // the whole server above its threshold
)

// Key identifies what is being watched. A process is identified by PID and
// start time together, so a reused PID is a different process.
type Key struct {
	Rule  Rule
	PID   int32
	Start uint64
}

// ProcSample is one process in one observation.
type ProcSample struct {
	PID   int32
	Start uint64
	CPU   float64 // percent of one core
	Known bool    // false until the process has been measured twice
}

// Observation is one sample of the server. Kernel threads and the watcher
// itself must already be left out by the caller.
type Observation struct {
	TotalCPU   float64 // percent of the whole machine, 0-100
	TotalKnown bool
	Procs      []ProcSample
}

// Kind is the type of a notice.
type Kind int

const (
	KindAlert Kind = iota // a threshold was exceeded for the whole window
	KindClear             // the incident is over
)

// Reason says why an incident ended.
type Reason int

const (
	ReasonBelow  Reason = iota // the 1-minute average dropped below the threshold
	ReasonExited               // the process is gone
)

// Notice is a mail that has to be sent. It stays pending until Sent is called,
// so a failed send is retried by simply asking for Pending again.
type Notice struct {
	ID        int
	Kind      Kind
	Key       Key
	Avg       float64       // alert: average over the window; clear: recent average
	Threshold float64       // percent
	Window    time.Duration // the configured duration
	Lasted    time.Duration // clear only: how long the incident lasted
	Reason    Reason        // clear only
}

// tracker is the rolling window of one watched thing.
type tracker struct {
	th      config.Threshold
	samples []float64 // newest last, never longer than the window (or the recent length)
	active  bool      // an incident is going on
	steps   int       // samples seen since the incident started
	alertID int       // ID of the alert notice of the current incident
}

// Engine applies the rules. It keeps all state in memory.
type Engine struct {
	process, total config.Threshold
	interval       time.Duration

	trackers map[Key]*tracker
	pending  []Notice // not sent yet, oldest first
	created  []Notice // made by the current Step
	nextID   int
}

// NewEngine creates an engine for the two CPU thresholds.
func NewEngine(process, total config.Threshold, interval time.Duration) *Engine {
	return &Engine{
		process:  process,
		total:    total,
		interval: interval,
		trackers: map[Key]*tracker{},
	}
}

// windowLen is the number of samples that make up a duration (at least 1).
func (e *Engine) windowLen(d time.Duration) int {
	n := int((d + e.interval - 1) / e.interval) // round up
	return max(n, 1)
}

// Step feeds one observation into the rules and returns the notices that were
// created by it (also available through Pending).
func (e *Engine) Step(obs Observation) []Notice {
	e.created = nil

	if obs.TotalKnown {
		e.feed(Key{Rule: RuleTotal}, e.total, obs.TotalCPU)
	}

	live := make(map[Key]bool, len(obs.Procs))
	for _, p := range obs.Procs {
		key := Key{Rule: RuleProcess, PID: p.PID, Start: p.Start}
		live[key] = true
		if p.Known {
			e.feed(key, e.process, p.CPU)
		}
	}

	// A process that is not in the observation any more has exited.
	// (Collect the keys first: the map is changed while clearing.)
	var gone []Key
	for key := range e.trackers {
		if key.Rule == RuleProcess && !live[key] {
			gone = append(gone, key)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].PID < gone[j].PID })
	for _, key := range gone {
		t := e.trackers[key]
		if t.active {
			e.end(key, t, ReasonExited)
		}
		delete(e.trackers, key)
	}

	// Report only notices that still exist: an alert that was created and
	// dropped within the same step (its incident already over) is not news.
	var out []Notice
	for _, n := range e.created {
		for _, p := range e.pending {
			if p.ID == n.ID {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// feed adds one sample to a tracker and fires or clears if needed.
func (e *Engine) feed(key Key, th config.Threshold, v float64) {
	t, ok := e.trackers[key]
	if !ok {
		t = &tracker{th: th}
		e.trackers[key] = t
	}

	n := e.windowLen(th.Duration)
	t.samples = append(t.samples, v)
	if keep := max(n, recentSamples); len(t.samples) > keep {
		t.samples = t.samples[len(t.samples)-keep:]
	}

	if t.active {
		t.steps++
		if recent := mean(last(t.samples, recentSamples)); recent < th.Percent {
			e.end(key, t, ReasonBelow)
		}
		return
	}

	// Only a full window can fire: a short spike must not count.
	if len(t.samples) < n {
		return
	}
	if avg := mean(last(t.samples, n)); avg >= th.Percent {
		t.active = true
		t.steps = 0
		t.alertID = e.add(Notice{
			Kind: KindAlert, Key: key, Avg: avg,
			Threshold: th.Percent, Window: th.Duration,
		})
	}
}

// end closes an incident. If its alert mail was never sent, nobody knows about
// it, so the alert is dropped and no all-clear is sent either.
func (e *Engine) end(key Key, t *tracker, reason Reason) {
	recent := mean(last(t.samples, recentSamples))
	t.active = false
	t.samples = nil // the window has to fill again before the next alert

	if e.drop(t.alertID) {
		return
	}
	e.add(Notice{
		Kind: KindClear, Key: key, Avg: recent,
		Threshold: t.th.Percent, Window: t.th.Duration,
		Lasted: time.Duration(t.steps) * e.interval, Reason: reason,
	})
}

// Pending returns the notices that still have to be sent, oldest first.
func (e *Engine) Pending() []Notice {
	return append([]Notice(nil), e.pending...)
}

// Sent marks a notice as delivered. Unknown IDs are ignored.
func (e *Engine) Sent(id int) { e.drop(id) }

// Active returns the keys that are in an incident right now.
func (e *Engine) Active() []Key {
	var keys []Key
	for key, t := range e.trackers {
		if t.active {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Rule != keys[j].Rule {
			return keys[i].Rule < keys[j].Rule
		}
		return keys[i].PID < keys[j].PID
	})
	return keys
}

func (e *Engine) add(n Notice) int {
	e.nextID++
	n.ID = e.nextID
	e.pending = append(e.pending, n)
	e.created = append(e.created, n)
	return n.ID
}

// drop removes a pending notice and reports whether it was there.
func (e *Engine) drop(id int) bool {
	for i, n := range e.pending {
		if n.ID == id {
			e.pending = append(e.pending[:i], e.pending[i+1:]...)
			return true
		}
	}
	return false
}

func last(s []float64, n int) []float64 {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func mean(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += v
	}
	return sum / float64(len(s))
}
