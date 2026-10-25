package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"eternalshell/internal/collector"
	"eternalshell/internal/domains"
	"eternalshell/internal/format"
)

// DomainResult is the check of one domain and the project it belongs to.
type DomainResult struct {
	Host    string
	Project string
	Result  domains.Result
}

// Data is everything the report text is made of.
type Data struct {
	Name string    // server.name from the config
	Now  time.Time // when the report is made

	Prev *State  // the previous report; nil if there is none
	Diff Changes // what changed since then

	// Watcher is true when the data comes from a running watcher. Only the
	// watcher has a CPU history and a list of alerts; WatcherSince is when it
	// began collecting.
	Watcher      bool
	WatcherSince time.Time
	CPU          *CPUStats
	Incidents    []Incident

	DiskAlertPercent float64 // alerts.disk.percent
	Mounts           []collector.Mount
	Containers       []collector.Container
	Disk             *collector.DiskUsage
	DiskErr          error
	Domains          []DomainResult

	Uptime   time.Duration
	BootTime time.Time
}

// attentionItem is one line under "needs attention".
type attentionItem struct{ text string }

// noiseBytes: a change smaller than this is shown as "no change".
const noiseBytes = 1 << 20

// topProjects is how many projects the report lists.
const topProjects = 5

const timeFormat = "Mon 02 Jan 15:04"

// Render makes the subject and the plain text body of the report.
func Render(d Data) (subject, body string) {
	items := d.attention()
	switch len(items) {
	case 0:
		subject = fmt.Sprintf("[EternalShell] %s report: all OK", d.Name)
	case 1:
		subject = fmt.Sprintf("[EternalShell] %s report: 1 thing needs attention", d.Name)
	default:
		subject = fmt.Sprintf("[EternalShell] %s report: %d things need attention", d.Name, len(items))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s - %s\n", d.Name, d.Now.Format(timeFormat))
	b.WriteString(d.period() + "\n")

	if len(items) > 0 {
		b.WriteString("\nNEEDS ATTENTION\n")
		for _, it := range items {
			fmt.Fprintf(&b, "  - %s\n", it.text)
		}
	}

	d.writeAlerts(&b)
	d.writeDisk(&b)
	d.writeProjects(&b)
	d.writeContainers(&b)
	d.writeCPU(&b)
	d.writeUptime(&b)
	return subject, b.String()
}

// period says what time the report covers.
func (d Data) period() string {
	if d.Prev == nil {
		return "No previous report to compare."
	}
	s := "Since the previous report on " + d.Prev.Time.Local().Format(timeFormat) + "."
	if d.Watcher && d.WatcherSince.After(d.Prev.Time) {
		s += " The watcher only started " + d.WatcherSince.Format(timeFormat) + ", so alerts and CPU cover less."
	}
	return s
}

// watchSince is the start of what the watcher's own data (alerts, CPU) covers.
func (d Data) watchSince() time.Time {
	if d.Prev != nil && d.Prev.Time.After(d.WatcherSince) {
		return d.Prev.Time
	}
	return d.WatcherSince
}

// containerLabel names a container for the report: project/service or the name.
func containerLabel(c collector.Container) string {
	if c.Project != "" {
		name := c.Service
		if name == "" {
			name = c.Name
		}
		return c.Project + "/" + name
	}
	return c.Name
}

// attention collects the things that need a look, most important first.
func (d Data) attention() []attentionItem {
	var items []attentionItem

	cs := append([]collector.Container(nil), d.Containers...)
	sort.SliceStable(cs, func(i, j int) bool { return containerLabel(cs[i]) < containerLabel(cs[j]) })
	for _, c := range cs {
		if why := c.Attention(); why != "" {
			items = append(items, attentionItem{fmt.Sprintf("container %s: %s", containerLabel(c), why)})
		}
	}

	// Mounts at or above the alert threshold. An alert of the period for the same
	// mount is told here, not a second time below.
	explained := map[string]bool{}
	for _, m := range d.Mounts {
		if d.DiskAlertPercent <= 0 || m.UsedPercent < d.DiskAlertPercent {
			continue
		}
		text := fmt.Sprintf("disk %s: %s used (alert threshold %s), %s free",
			m.Path, format.Percent(m.UsedPercent), format.Percent(d.DiskAlertPercent), format.Bytes(m.Total-m.Used))
		key := "disk:" + m.Path
		for _, inc := range d.Incidents {
			if inc.Key == key {
				text += "; alert fired " + inc.FiredAt.Format("Mon 15:04")
				if inc.Active() {
					text += ", still active"
				}
				explained[key] = true
				break
			}
		}
		items = append(items, attentionItem{text})
	}

	for _, dr := range d.Domains {
		r := dr.Result
		var problems []string
		if r.StatusLevel() == domains.LevelBad {
			problems = append(problems, "status "+r.StatusText())
		}
		switch r.CertLevel(d.Now) {
		case domains.LevelBad:
			problems = append(problems, "certificate "+r.CertText(d.Now))
		case domains.LevelWarn:
			problems = append(problems, fmt.Sprintf("certificate expires in %d d", r.DaysLeft(d.Now)))
		}
		if len(problems) > 0 {
			items = append(items, attentionItem{fmt.Sprintf("domain %s (%s): %s", dr.Host, dr.Project, strings.Join(problems, ", "))})
		}
	}

	for _, inc := range d.Incidents {
		if explained[inc.Key] {
			continue
		}
		items = append(items, attentionItem{"alert " + incidentText(inc)})
	}
	return items
}

// incidentText: "cpu-total (avg 95% over 10m): fired Mon 03:12, cleared 03:40 (28 min)".
func incidentText(inc Incident) string {
	s := inc.What
	if inc.Detail != "" {
		s += " (" + inc.Detail + ")"
	}
	s += ": fired " + inc.FiredAt.Format("Mon 15:04")
	if inc.Active() {
		return s + ", still active"
	}
	return s + ", cleared " + inc.ClearedAt.Format("15:04") + " (" + duration(inc.ClearedAt.Sub(inc.FiredAt)) + ")"
}

func (d Data) writeAlerts(b *strings.Builder) {
	if !d.Watcher {
		b.WriteString("\nALERTS IN THE PERIOD\n  n/a (collected only by the running watcher)\n")
		return
	}
	fmt.Fprintf(b, "\nALERTS IN THE PERIOD (since %s)\n", d.watchSince().Format(timeFormat))
	if len(d.Incidents) == 0 {
		b.WriteString("  none\n")
		return
	}
	for _, inc := range d.Incidents {
		fmt.Fprintf(b, "  %s\n", incidentText(inc))
	}
}

func (d Data) writeDisk(b *strings.Builder) {
	b.WriteString("\n" + title("DISK", d.Diff.HasPrev) + "\n")
	if len(d.Mounts) == 0 {
		b.WriteString("  no mounts could be read\n")
	}
	for _, m := range d.Mounts {
		fmt.Fprintf(b, "  %-16s %8s of %8s  %4s%s\n", m.Path, format.Bytes(m.Used), format.Bytes(m.Total),
			format.Percent(m.UsedPercent), d.mountChange(m))
	}
	for _, p := range d.Diff.GoneMounts {
		fmt.Fprintf(b, "  %-16s no longer mounted (it was at the previous report)\n", p)
	}
}

// mountChange is the text after a mount line: the change since the last report.
func (d Data) mountChange(m collector.Mount) string {
	if !d.Diff.HasPrev {
		return ""
	}
	for _, p := range d.Diff.NewMounts {
		if p == m.Path {
			return "   (new mount)"
		}
	}
	if delta, ok := d.Diff.MountUsed[m.Path]; ok {
		return "   " + changeText(delta)
	}
	return ""
}

func (d Data) writeProjects(b *strings.Builder) {
	b.WriteString("\n" + title("TOP PROJECTS BY DISK USE", d.Diff.DiskCompared) + "\n")
	u := d.Disk
	if u == nil {
		if d.DiskErr != nil {
			fmt.Fprintf(b, "  not available: %v\n", d.DiskErr)
		} else {
			b.WriteString("  not measured yet\n")
		}
		return
	}
	shown := 0
	for _, p := range u.Projects {
		if shown == topProjects || p.Total() == 0 {
			break
		}
		shown++
		key := projectKey(p.Name)
		name := p.Name
		if p.Standalone {
			key, name = containerKey(p.Name), p.Name+" (container)"
		}
		fmt.Fprintf(b, "  %-24.24s %9s%s\n", name, format.BytesPartial(p.Total(), !p.Complete()), d.projectChange(key))
	}
	if shown == 0 {
		b.WriteString("  no containers\n")
	}

	fmt.Fprintf(b, "\n%s\n  %s (unused volumes %s + dangling images %s)",
		title("RECLAIMABLE", d.Diff.DiskCompared), format.Bytes(uint64(u.Reclaimable)), format.Bytes(uint64(u.UnusedVolumes)), format.Bytes(uint64(u.DanglingImg)))
	if d.Diff.DiskCompared {
		b.WriteString("   " + changeText(d.Diff.ReclaimDelta))
	}
	b.WriteString("\n")
}

func (d Data) projectChange(key string) string {
	if !d.Diff.DiskCompared {
		return ""
	}
	for _, k := range d.Diff.NewProjects {
		if k == key {
			return "   (new)"
		}
	}
	if delta, ok := d.Diff.ProjectDelta[key]; ok {
		return "   " + changeText(delta)
	}
	return ""
}

func (d Data) writeContainers(b *strings.Builder) {
	b.WriteString("\nCONTAINERS\n")
	if len(d.Containers) == 0 {
		b.WriteString("  none (or Docker is not reachable)\n")
		return
	}
	type count struct{ running, total int }
	groups := map[string]*count{}
	for _, c := range d.Containers {
		g := c.Project
		if g == "" {
			g = standaloneGroup
		}
		if groups[g] == nil {
			groups[g] = &count{}
		}
		groups[g].total++
		if c.Running() {
			groups[g].running++
		}
	}
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		c := groups[g]
		line := fmt.Sprintf("%d running", c.running)
		if c.total > c.running {
			line += fmt.Sprintf(", %d stopped", c.total-c.running)
		}
		fmt.Fprintf(b, "  %-24.24s %s\n", g, line)
	}

	ch := d.Diff
	if !ch.HasPrev {
		return
	}
	if len(ch.AddedGroups)+len(ch.AddedContainers) > 0 {
		fmt.Fprintf(b, "  new since the previous report: %s\n", strings.Join(append(append([]string{}, ch.AddedGroups...), ch.AddedContainers...), ", "))
	}
	if len(ch.RemovedGroups)+len(ch.RemovedContainers) > 0 {
		fmt.Fprintf(b, "  removed since the previous report: %s\n", strings.Join(append(append([]string{}, ch.RemovedGroups...), ch.RemovedContainers...), ", "))
	}
}

func (d Data) writeCPU(b *strings.Builder) {
	switch {
	case !d.Watcher:
		b.WriteString("\nCPU\n  n/a (collected only by the running watcher)\n")
	case d.CPU == nil:
		b.WriteString("\nCPU\n  no samples yet\n")
	default:
		fmt.Fprintf(b, "\nCPU (since %s)\n  average %s, peak %s at %s\n", d.watchSince().Format(timeFormat),
			format.Percent(d.CPU.Avg), format.Percent(d.CPU.Peak), d.CPU.PeakAt.Format("Mon 15:04"))
	}
}

func (d Data) writeUptime(b *strings.Builder) {
	fmt.Fprintf(b, "\nUPTIME\n  up %s", duration(d.Uptime))
	switch {
	case !d.Diff.HasPrev:
	case d.Diff.Rebooted:
		fmt.Fprintf(b, ", rebooted since the previous report (boot at %s)", d.BootTime.Format(timeFormat))
	default:
		b.WriteString(", no reboot since the previous report")
	}
	b.WriteString("\n")
}

// changeText: "+1.2G", "-300.0M", or "no change".
func changeText(delta int64) string {
	abs := delta
	if abs < 0 {
		abs = -abs
	}
	if abs < noiseBytes {
		return "no change"
	}
	sign := "+"
	if delta < 0 {
		sign = "-"
	}
	return sign + format.Bytes(uint64(abs))
}

// sinceNote is added to the title of a section that shows changes.
const sinceNote = " (change since the previous report)"

// title returns a section title, with the note when changes are shown.
func title(name string, withChanges bool) string {
	if withChanges {
		return name + sinceNote
	}
	return name
}

// duration formats a length of time for people: "12 d 3 h", "3 h 5 min", "28 min".
func duration(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%d d %d h", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d min", hours, mins)
	default:
		return fmt.Sprintf("%d min", mins)
	}
}
