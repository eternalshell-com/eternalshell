package watch

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"eternalshell/internal/collector"
	"eternalshell/internal/config"
	"eternalshell/internal/mail"
)

// SendFunc sends one mail. mail.Send is the real one.
type SendFunc func(ctx context.Context, cfg config.SMTP, password string, msg mail.Message) error

// Options are the inputs of Run.
type Options struct {
	Config    *config.Config
	Password  string // SMTP password, read from the environment by the caller
	Collector *collector.Collector
	Out       io.Writer // one line per event
	Send      SendFunc  // defaults to mail.Send
}

// Run watches the server until ctx is cancelled. It never returns because of a
// failed collection or mail: those are logged and tried again on the next tick.
func Run(ctx context.Context, o Options) error {
	send := o.Send
	if send == nil {
		send = mail.Send
	}
	cfg := o.Config
	cpu := cfg.Alerts.CPU
	eng := NewEngine(cpu.Process, cpu.Total, Interval)
	self := os.Getpid()
	lg := logger{out: o.Out}

	lg.event("INFO", "watching: cpu-process >= %s of one core for %s, cpu-total >= %s for %s, sampling every %s",
		pct(cpu.Process.Percent), duration(cpu.Process.Duration), pct(cpu.Total.Percent), duration(cpu.Total.Duration), Interval)
	lg.event("INFO", "mail to %s via %s:%d (%s)",
		strings.Join(cfg.SMTP.To, ", "), cfg.SMTP.Host, cfg.SMTP.Port, mail.TransportFor(cfg.SMTP.Port))
	for _, th := range []struct {
		name string
		d    time.Duration
	}{{"alerts.cpu.process.duration", cpu.Process.Duration}, {"alerts.cpu.total.duration", cpu.Total.Duration}} {
		if th.d < Interval {
			lg.event("WARN", "%s is shorter than the sampling interval (%s); it counts as one sample", th.name, Interval)
		}
	}

	// known remembers the last time we saw each process in an incident, so the
	// all-clear mail can still describe a process that has already exited.
	known := map[Key]collector.Process{}

	ticker := time.NewTicker(Interval)
	defer ticker.Stop()

	// The first Collect only takes the first sample (CPU values are deltas), so
	// it is done right away instead of waiting a full interval.
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				lg.event("INFO", "stopped")
				return nil
			case <-ticker.C:
			}
		}

		snap, err := o.Collector.Collect()
		if err != nil {
			lg.event("ERROR", "collecting data failed: %v (will retry)", err)
			continue
		}

		for _, n := range eng.Step(observe(snap, self)) {
			level := "ALERT"
			if n.Kind == KindClear {
				level = "CLEAR"
			}
			lg.event(level, "%s", describe(n, known, snap))
		}
		remember(known, eng, snap)

		for _, n := range eng.Pending() {
			if ctx.Err() != nil {
				break
			}
			d := Details{Server: cfg.Server.Name, Now: time.Now(), Snap: snap, Proc: processFor(n, known, snap)}
			if err := send(ctx, cfg.SMTP, o.Password, Message(n, d)); err != nil {
				lg.event("ERROR", "mail failed for %s: %s (will retry in %s)", noticeLabel(n), mail.Explain(err, cfg.SMTP), Interval)
				continue
			}
			eng.Sent(n.ID)
			lg.event("INFO", "mail sent: %s", noticeLabel(n))
		}
		forget(known, eng)
	}
}

// observe turns a snapshot into the engine's input. Kernel threads and the
// watcher itself are left out.
func observe(snap *collector.Snapshot, self int) Observation {
	obs := Observation{TotalCPU: snap.CPUPercent, TotalKnown: snap.CPUKnown}
	for _, p := range snap.Processes {
		if p.Kernel || int(p.PID) == self {
			continue
		}
		obs.Procs = append(obs.Procs, ProcSample{PID: p.PID, Start: p.Start, CPU: p.CPU, Known: p.CPUKnown})
	}
	return obs
}

// remember stores the current state of every process that is in an incident.
func remember(known map[Key]collector.Process, eng *Engine, snap *collector.Snapshot) {
	active := eng.Active()
	if len(active) == 0 {
		return
	}
	byKey := make(map[Key]collector.Process, len(active))
	for _, p := range snap.Processes {
		byKey[Key{Rule: RuleProcess, PID: p.PID, Start: p.Start}] = p
	}
	for _, key := range active {
		if p, ok := byKey[key]; ok {
			known[key] = p
		}
	}
}

// forget drops remembered processes that have no incident and no pending mail.
func forget(known map[Key]collector.Process, eng *Engine) {
	if len(known) == 0 {
		return
	}
	keep := map[Key]bool{}
	for _, key := range eng.Active() {
		keep[key] = true
	}
	for _, n := range eng.Pending() {
		keep[n.Key] = true
	}
	for key := range known {
		if !keep[key] {
			delete(known, key)
		}
	}
}

// processFor finds the process a notice is about: the current one, or the last
// one seen. Nil for the total rule or a process that was never seen.
func processFor(n Notice, known map[Key]collector.Process, snap *collector.Snapshot) *collector.Process {
	if n.Key.Rule != RuleProcess {
		return nil
	}
	for i := range snap.Processes {
		p := &snap.Processes[i]
		if p.PID == n.Key.PID && p.Start == n.Key.Start {
			return p
		}
	}
	if p, ok := known[n.Key]; ok {
		return &p
	}
	return nil
}

// describe is the log line for a new notice.
func describe(n Notice, known map[Key]collector.Process, snap *collector.Snapshot) string {
	what := string(n.Key.Rule)
	if p := processFor(n, known, snap); p != nil {
		what = fmt.Sprintf("%s %s pid=%d", n.Key.Rule, p.Name, p.PID)
	} else if n.Key.Rule == RuleProcess {
		what = fmt.Sprintf("%s pid=%d", n.Key.Rule, n.Key.PID)
	}
	if n.Kind == KindAlert {
		return fmt.Sprintf("%s avg=%s over %s (threshold %s)", what, pct(n.Avg), duration(n.Window), pct(n.Threshold))
	}
	reason := "below threshold"
	if n.Reason == ReasonExited {
		reason = "process exited"
	}
	return fmt.Sprintf("%s %s, recent avg=%s", what, reason, pct(n.Avg))
}

func noticeLabel(n Notice) string {
	kind := "alert"
	if n.Kind == KindClear {
		kind = "all-clear"
	}
	if n.Key.Rule == RuleProcess {
		return fmt.Sprintf("%s %s pid=%d", kind, n.Key.Rule, n.Key.PID)
	}
	return fmt.Sprintf("%s %s", kind, n.Key.Rule)
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v) }

// logger writes one line per event: UTC time, level, text.
type logger struct{ out io.Writer }

func (l logger) event(level, format string, args ...any) {
	fmt.Fprintf(l.out, "%s %-5s %s\n", time.Now().UTC().Format(time.RFC3339), level, fmt.Sprintf(format, args...))
}
