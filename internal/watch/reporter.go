package watch

import (
	"context"
	"fmt"
	"time"

	"eternalshell/internal/collector"
	"eternalshell/internal/config"
	"eternalshell/internal/mail"
	"eternalshell/internal/report"
)

// reportRetry is how long to wait before sending a failed report again. The text
// is kept, so the domains are not checked a second time.
const reportRetry = 5 * time.Minute

// reporter sends the periodic status report from inside the watch loop. It lives
// in that single loop, so it needs no lock.
type reporter struct {
	enabled bool
	cfg     *config.Config
	lg      logger

	sched   report.Schedule
	journal *report.Journal // alerts and CPU samples of the period

	prev       *report.State // the previous report, for the comparison
	lastReport time.Time
	dueSince   time.Time // when the report first became due (waiting for the disk data)

	pending *pendingReport // built, but not delivered yet
	retryAt time.Time
}

type pendingReport struct {
	msg   mail.Message
	state report.State // saved once the mail was accepted
}

// newReporter reads the schedule and the saved state, and logs when the next
// report is due. With the report off (or a schedule that cannot be read) it does
// nothing.
func newReporter(ctx context.Context, cfg *config.Config, lg logger) *reporter {
	r := &reporter{cfg: cfg, lg: lg}
	if !cfg.Alerts.Report.Enabled {
		return r
	}
	sched, err := report.ScheduleFrom(cfg.Alerts.Report)
	if err != nil {
		lg.event("ERROR", "report disabled: %v", err)
		return r
	}
	r.enabled, r.sched = true, sched

	prev, err := report.LoadState()
	if err != nil {
		lg.event("WARN", "report: %v (continuing without a previous report)", err)
	}
	r.prev = prev
	now := time.Now()
	if prev != nil {
		r.lastReport = prev.Time
		lg.event("INFO", "report: %s, previous report %s, next at %s", sched, prev.Time.Format(time.RFC3339), sched.NextRun(now).Format(time.RFC3339))
	} else {
		lg.event("INFO", "report: %s, no previous report yet, the first one is sent at %s", sched, sched.NextRun(now).Format(time.RFC3339))
	}
	return r
}

// journalKey is the name under which an incident is noted in the journal.
func journalKey(k Key) string {
	switch k.Rule {
	case RuleProcess:
		return fmt.Sprintf("proc:%d:%d", k.PID, k.Start)
	case RuleDisk:
		return "disk:" + k.Mount
	default:
		return string(k.Rule)
	}
}

// note records an alert or an all-clear in the journal.
func (r *reporter) note(n Notice, known map[Key]collector.Process, snap *collector.Snapshot, at time.Time) {
	key := journalKey(n.Key)
	if n.Kind == KindClear {
		r.journal.Cleared(key, at)
		return
	}
	what := "cpu-total"
	detail := fmt.Sprintf("avg %s over %s", pct(n.Avg), duration(n.Window))
	switch n.Key.Rule {
	case RuleProcess:
		what = fmt.Sprintf("cpu-process pid %d", n.Key.PID)
		if p := processFor(n, known, snap); p != nil {
			what = fmt.Sprintf("cpu-process %s (pid %d)", p.Name, p.PID)
		}
	case RuleDisk:
		what = "disk " + n.Key.Mount
		detail = pct(n.Avg) + " used"
	}
	r.journal.Fired(key, what, detail, at)
}

// tick sends the report when it is due. It is called once per sample, and only
// does work when the schedule says so.
func (r *reporter) tick(ctx context.Context, o Options, send SendFunc, snap *collector.Snapshot, now time.Time) {
	if !r.enabled || now.Before(r.retryAt) || ctx.Err() != nil {
		return
	}

	if r.pending == nil {
		if !r.sched.Due(r.lastReport, now) {
			return
		}
		if r.dueSince.IsZero() {
			r.dueSince = now
		}
		// The first Docker disk usage can take a while after a restart; the
		// report is worth a short wait, but not forever.
		if snap.Disk == nil && snap.DiskErr == nil && now.Sub(r.dueSince) < diskWait {
			return
		}
		r.lg.event("INFO", "report: due, checking the domains and building it")
		d := report.Build(ctx, report.Input{Snap: snap, Config: r.cfg, Prev: r.prev, Journal: r.journal, Now: now})
		subject, body := report.Render(d)
		r.pending = &pendingReport{msg: mail.Message{Subject: subject, Body: body}, state: report.SnapshotState(d)}
	}

	if err := send(ctx, r.cfg.SMTP, o.Password, r.pending.msg); err != nil {
		r.retryAt = now.Add(reportRetry)
		r.lg.event("ERROR", "report mail failed: %s (will retry in %s)", mail.Explain(err, r.cfg.SMTP), reportRetry)
		return
	}
	r.lg.event("INFO", "report sent: %s", r.pending.msg.Subject)

	// Remember it in memory first: even if the file cannot be written, this
	// process will not send the same report again.
	st := r.pending.state
	r.prev, r.lastReport = &st, st.Time
	r.pending, r.dueSince, r.retryAt = nil, time.Time{}, time.Time{}
	r.journal.Reset(now)
	if err := st.Save(); err != nil {
		r.lg.event("WARN", "report: the state could not be saved (%v); the next report will not show changes", err)
	}
}
