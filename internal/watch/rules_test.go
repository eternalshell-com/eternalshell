package watch

import (
	"testing"
	"time"

	"eternalshell/internal/config"
)

// A 30 s window with a 10 s interval is 3 samples, so tests need no waiting.
func newTestEngine() *Engine {
	return NewEngine(
		config.Threshold{Percent: 80, Duration: 30 * time.Second},
		config.Threshold{Percent: 90, Duration: 30 * time.Second},
		10*time.Second,
	)
}

func procObs(pid int32, start uint64, cpu float64) Observation {
	return Observation{Procs: []ProcSample{{PID: pid, Start: start, CPU: cpu, Known: true}}}
}

func feedProc(e *Engine, n int, pid int32, start uint64, cpu float64) []Notice {
	var all []Notice
	for i := 0; i < n; i++ {
		all = append(all, e.Step(procObs(pid, start, cpu))...)
	}
	return all
}

func TestNoAlertBeforeWindowIsFull(t *testing.T) {
	e := newTestEngine()
	if got := feedProc(e, 2, 1, 100, 500); len(got) != 0 {
		t.Errorf("2 of 3 samples must not fire, got %+v", got)
	}
	if got := feedProc(e, 1, 1, 100, 500); len(got) != 1 || got[0].Kind != KindAlert {
		t.Errorf("the 3rd sample should fire, got %+v", got)
	}
}

func TestAlertUsesTheAverage(t *testing.T) {
	e := newTestEngine()
	// 200, 0, 0 averages 66 %: a short spike is not an incident
	e.Step(procObs(1, 100, 200))
	e.Step(procObs(1, 100, 0))
	if got := e.Step(procObs(1, 100, 0)); len(got) != 0 {
		t.Errorf("average 66 %% is below 80 %%, got %+v", got)
	}

	// 120, 100, 50 averages 90 %: fires although the last value is low
	e = newTestEngine()
	e.Step(procObs(1, 100, 120))
	e.Step(procObs(1, 100, 100))
	got := e.Step(procObs(1, 100, 50))
	if len(got) != 1 || got[0].Avg != 90 {
		t.Errorf("want one alert with avg 90, got %+v", got)
	}
}

func TestFiresOnlyOncePerIncident(t *testing.T) {
	e := newTestEngine()
	got := feedProc(e, 20, 1, 100, 300)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 notice over 20 high samples, got %d: %+v", len(got), got)
	}
	// still pending (not sent yet) and still just one
	if p := e.Pending(); len(p) != 1 {
		t.Errorf("pending = %+v", p)
	}
}

func TestFailedSendStaysPendingUntilSent(t *testing.T) {
	e := newTestEngine()
	notices := feedProc(e, 3, 1, 100, 300)
	id := notices[0].ID

	// the sender failed: asking again returns the same notice
	e.Step(procObs(1, 100, 300))
	p := e.Pending()
	if len(p) != 1 || p[0].ID != id {
		t.Fatalf("a failed notice must stay pending, got %+v", p)
	}

	e.Sent(id)
	if len(e.Pending()) != 0 {
		t.Error("a sent notice must be gone")
	}
	e.Sent(999) // unknown IDs are ignored
}

func TestAllClearWhenRecentAverageDrops(t *testing.T) {
	e := newTestEngine()
	notices := feedProc(e, 3, 1, 100, 300)
	e.Sent(notices[0].ID)

	// still high: no all-clear
	if got := feedProc(e, 5, 1, 100, 300); len(got) != 0 {
		t.Fatalf("no notice while it lasts, got %+v", got)
	}

	// then calm: the 6-sample average falls below 80 % after a few samples
	var clear *Notice
	for i := 0; i < 6 && clear == nil; i++ {
		for _, n := range e.Step(procObs(1, 100, 0)) {
			n := n
			clear = &n
		}
	}
	if clear == nil {
		t.Fatal("expected an all-clear")
	}
	if clear.Kind != KindClear || clear.Reason != ReasonBelow || clear.Avg >= 80 {
		t.Errorf("clear = %+v", clear)
	}
	if clear.Lasted <= 0 {
		t.Errorf("Lasted should be positive, got %v", clear.Lasted)
	}
}

func TestAllClearWhenProcessExits(t *testing.T) {
	e := newTestEngine()
	notices := feedProc(e, 3, 1, 100, 300)
	e.Sent(notices[0].ID)

	got := e.Step(Observation{}) // the process is not there any more
	if len(got) != 1 || got[0].Kind != KindClear || got[0].Reason != ReasonExited {
		t.Errorf("want one all-clear (exited), got %+v", got)
	}
}

func TestReusedPIDIsANewProcess(t *testing.T) {
	e := newTestEngine()
	notices := feedProc(e, 3, 42, 100, 300)
	e.Sent(notices[0].ID)

	// same PID, different start time: the old one exited, the new one starts empty
	got := e.Step(procObs(42, 200, 300))
	if len(got) != 1 || got[0].Kind != KindClear || got[0].Reason != ReasonExited || got[0].Key.Start != 100 {
		t.Errorf("old process should be cleared as exited, got %+v", got)
	}
	// the new process needs its own full window before it can fire
	if got := feedProc(e, 1, 42, 200, 300); len(got) != 0 {
		t.Errorf("2 of 3 samples for the new process must not fire, got %+v", got)
	}
	if got := feedProc(e, 1, 42, 200, 300); len(got) != 1 || got[0].Kind != KindAlert || got[0].Key.Start != 200 {
		t.Errorf("3rd sample should fire for the new process, got %+v", got)
	}
}

func TestWindowIsResetAfterClear(t *testing.T) {
	e := newTestEngine()
	notices := feedProc(e, 3, 1, 100, 300)
	e.Sent(notices[0].ID)

	cleared := false
	for i := 0; i < 6 && !cleared; i++ {
		for _, n := range e.Step(procObs(1, 100, 0)) {
			cleared = cleared || n.Kind == KindClear
		}
	}
	if !cleared {
		t.Fatal("expected a clear")
	}
	for _, n := range e.Pending() {
		e.Sent(n.ID)
	}

	// busy again: it must not fire at once, the window has to fill first
	if got := feedProc(e, 2, 1, 100, 300); len(got) != 0 {
		t.Errorf("must not re-fire from old samples, got %+v", got)
	}
	if got := feedProc(e, 1, 1, 100, 300); len(got) != 1 || got[0].Kind != KindAlert {
		t.Errorf("should fire once the window is full again, got %+v", got)
	}
}

func TestUnknownSamplesAreSkipped(t *testing.T) {
	e := newTestEngine()
	unknown := Observation{Procs: []ProcSample{{PID: 1, Start: 100, CPU: 999, Known: false}}}

	for i := 0; i < 10; i++ {
		if got := e.Step(unknown); len(got) != 0 {
			t.Fatalf("unknown samples must not fire, got %+v", got)
		}
	}

	// unknown is not the same as gone: an active incident is not cleared by it
	notices := feedProc(e, 3, 1, 100, 300)
	e.Sent(notices[0].ID)
	if got := e.Step(unknown); len(got) != 0 {
		t.Errorf("an unknown sample must not end the incident, got %+v", got)
	}
}

func TestUnsentAlertIsDroppedWhenItEnds(t *testing.T) {
	e := newTestEngine()
	feedProc(e, 3, 1, 100, 300) // alert created, mail never delivered

	e.Step(Observation{}) // the process exits
	if p := e.Pending(); len(p) != 0 {
		t.Errorf("neither alert nor all-clear should remain, got %+v", p)
	}
}

func TestTotalCPU(t *testing.T) {
	e := newTestEngine()
	total := func(v float64, known bool) []Notice {
		return e.Step(Observation{TotalCPU: v, TotalKnown: known})
	}

	total(95, true)
	total(95, false) // unknown: skipped
	if got := total(95, true); len(got) != 0 {
		t.Fatalf("2 known samples must not fire, got %+v", got)
	}
	got := total(95, true)
	if len(got) != 1 || got[0].Kind != KindAlert || got[0].Key.Rule != RuleTotal || got[0].Threshold != 90 {
		t.Errorf("want a total alert at 90 %%, got %+v", got)
	}
}

func TestActiveKeys(t *testing.T) {
	e := newTestEngine()
	feedProc(e, 3, 7, 100, 300)
	keys := e.Active()
	if len(keys) != 1 || keys[0].PID != 7 {
		t.Errorf("Active = %+v", keys)
	}
}
