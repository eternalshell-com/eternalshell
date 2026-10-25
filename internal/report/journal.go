package report

import "time"

// maxIncidents bounds the journal; the oldest finished incidents go first.
const maxIncidents = 200

// Incident is one alert that fired, and when it cleared (zero while it lasts).
type Incident struct {
	Key       string // what the alert is about, e.g. "disk:/" or "cpu-total"
	What      string // short text, e.g. "disk /" or "cpu-process xmrig (pid 48211)"
	Detail    string // e.g. "91% used" or "avg 382% over 15m"
	FiredAt   time.Time
	ClearedAt time.Time
}

// Active reports whether the incident has not been cleared yet.
func (i Incident) Active() bool { return i.ClearedAt.IsZero() }

// CPUStats is the total CPU over a stretch of time.
type CPUStats struct {
	Avg     float64
	Peak    float64
	PeakAt  time.Time
	Samples int
	Since   time.Time
}

// Journal is what the watcher remembers between two reports: the alerts that
// fired and the samples of the total CPU. It lives in memory only and belongs to
// the watcher's single loop, so it has no lock.
type Journal struct {
	Started time.Time // when the watcher began; everything here is from after that

	since     time.Time // start of the current CPU window
	incidents []Incident
	cpuSum    float64
	cpuN      int
	cpuPeak   float64
	cpuPeakAt time.Time
}

// NewJournal starts an empty journal.
func NewJournal(now time.Time) *Journal {
	return &Journal{Started: now, since: now}
}

// Fired records an alert that started.
func (j *Journal) Fired(key, what, detail string, at time.Time) {
	j.incidents = append(j.incidents, Incident{Key: key, What: what, Detail: detail, FiredAt: at})
	for len(j.incidents) > maxIncidents {
		drop := -1
		for i, inc := range j.incidents {
			if !inc.Active() {
				drop = i
				break
			}
		}
		if drop < 0 {
			drop = 0 // all are active: the oldest goes
		}
		j.incidents = append(j.incidents[:drop], j.incidents[drop+1:]...)
	}
}

// Cleared records that the newest active alert with this key is over.
func (j *Journal) Cleared(key string, at time.Time) {
	for i := len(j.incidents) - 1; i >= 0; i-- {
		if j.incidents[i].Key == key && j.incidents[i].Active() {
			j.incidents[i].ClearedAt = at
			return
		}
	}
}

// AddCPU records one sample of the total CPU (percent of the whole machine).
func (j *Journal) AddCPU(percent float64, at time.Time) {
	j.cpuSum += percent
	j.cpuN++
	if percent > j.cpuPeak || j.cpuN == 1 {
		j.cpuPeak, j.cpuPeakAt = percent, at
	}
}

// CPU returns the average and peak since the window started, or nil if there is
// no sample yet.
func (j *Journal) CPU() *CPUStats {
	if j.cpuN == 0 {
		return nil
	}
	return &CPUStats{Avg: j.cpuSum / float64(j.cpuN), Peak: j.cpuPeak, PeakAt: j.cpuPeakAt, Samples: j.cpuN, Since: j.since}
}

// IncidentsSince returns the incidents that are relevant for a period starting
// at t: those that fired or cleared since then, and those still going on.
func (j *Journal) IncidentsSince(t time.Time) []Incident {
	var out []Incident
	for _, inc := range j.incidents {
		if inc.Active() || !inc.FiredAt.Before(t) || !inc.ClearedAt.Before(t) {
			out = append(out, inc)
		}
	}
	return out
}

// Reset starts the next period after a report was sent: the CPU window begins
// again, and finished incidents are forgotten. Incidents that still last stay.
func (j *Journal) Reset(now time.Time) {
	j.since = now
	j.cpuSum, j.cpuN, j.cpuPeak, j.cpuPeakAt = 0, 0, 0, time.Time{}
	kept := j.incidents[:0]
	for _, inc := range j.incidents {
		if inc.Active() {
			kept = append(kept, inc)
		}
	}
	j.incidents = kept
}
