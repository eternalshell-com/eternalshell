package watch

import (
	"fmt"
	"strings"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
	"eternalshell/internal/mailhtml"
)

// htmlMessage builds the HTML version of the mail for a notice. It shows the
// same facts as the text (textMessage). It returns "" if the page could not be
// drawn; the plain text is then enough.
func htmlMessage(n Notice, d Details) string {
	var doc mailhtml.Doc
	switch {
	case n.Key.Rule == RuleDisk && n.Kind == KindClear:
		doc = diskClearDoc(n, d)
	case n.Key.Rule == RuleDisk:
		doc = diskAlertDoc(n, d)
	case n.Kind == KindClear:
		doc = cpuClearDoc(n, d)
	default:
		doc = cpuAlertDoc(n, d)
	}
	return mailhtml.Render(doc)
}

const factTimeFormat = "2006-01-02 15:04:05 UTC"

func cpuAlertDoc(n Notice, d Details) mailhtml.Doc {
	doc := mailhtml.New(d.Server, d.Now)
	doc.Badge = mailhtml.Badge{Level: mailhtml.LevelBad, Text: "CPU alert"}

	var what, value, threshold string
	if n.Key.Rule == RuleProcess {
		name, pid := procLabel(n, d)
		doc.Title = fmt.Sprintf("%s (PID %s) at %s CPU for %s", name, pid, format.Percent(n.Avg), duration(n.Window))
		what = fmt.Sprintf("%s (PID %s)", name, pid)
		value = fmt.Sprintf("%s of one core, average over the last %s", format.Percent(n.Avg), duration(n.Window))
		threshold = fmt.Sprintf("%s of one core for %s", format.Percent(n.Threshold), duration(n.Window))
	} else {
		doc.Title = fmt.Sprintf("Server CPU at %s for %s", format.Percent(n.Avg), duration(n.Window))
		what = "The whole server"
		value = fmt.Sprintf("%s, average over the last %s", format.Percent(n.Avg), duration(n.Window))
		threshold = fmt.Sprintf("%s for %s", format.Percent(n.Threshold), duration(n.Window))
	}
	doc.Lead = "You will get one all-clear mail when this is over. No further mails about this incident until then."

	doc.Sections = append(doc.Sections, mailhtml.Section{Heading: "What happened", Blocks: []mailhtml.Block{
		mailhtml.Facts(
			mailhtml.Fact{Label: "What", Value: what},
			mailhtml.Fact{Label: "Value", Value: value, Mono: true, Tone: mailhtml.ToneUp},
			mailhtml.Fact{Label: "Threshold", Value: threshold, Mono: true},
			mailhtml.Fact{Label: "Detected", Value: d.Now.UTC().Format(factTimeFormat), Mono: true},
		),
	}})
	if n.Key.Rule == RuleProcess {
		if sec, ok := processSection(d.Proc); ok {
			doc.Sections = append(doc.Sections, sec)
		}
	}
	doc.Sections = append(doc.Sections, serverSection(d))

	heading := "Processes using the most CPU right now"
	if n.Key.Rule == RuleTotal {
		heading = "Likely cause: the processes using the most CPU right now"
	}
	doc.Sections = append(doc.Sections, topProcessesSection(heading, d.Snap))
	return doc
}

func cpuClearDoc(n Notice, d Details) mailhtml.Doc {
	doc := mailhtml.New(d.Server, d.Now)
	doc.Badge = mailhtml.Badge{Level: mailhtml.LevelOK, Text: "All clear"}

	var ended, reason string
	if n.Key.Rule == RuleProcess {
		name, pid := procLabel(n, d)
		ended = fmt.Sprintf("%s (PID %s)", name, pid)
		if n.Reason == ReasonExited {
			doc.Title = fmt.Sprintf("%s (PID %s) has exited", name, pid)
			reason = "The process has exited. The high CPU use is over."
		} else {
			doc.Title = fmt.Sprintf("%s (PID %s) is calm again", name, pid)
			reason = fmt.Sprintf("Back below %s of one core (average of the last minute: %s).", format.Percent(n.Threshold), format.Percent(n.Avg))
		}
	} else {
		ended = "The whole server"
		doc.Title = fmt.Sprintf("Server CPU back to %s", format.Percent(n.Avg))
		reason = fmt.Sprintf("Back below %s (average of the last minute: %s).", format.Percent(n.Threshold), format.Percent(n.Avg))
	}

	facts := []mailhtml.Fact{
		{Label: "What ended", Value: ended},
		{Label: "Why", Value: reason, Tone: mailhtml.ToneDown},
	}
	if n.Lasted > 0 {
		facts = append(facts, mailhtml.Fact{Label: "Lasted", Value: "about " + duration(n.Lasted) + " after the alert", Mono: true})
	}
	doc.Sections = append(doc.Sections, mailhtml.Section{Heading: "What happened", Blocks: []mailhtml.Block{mailhtml.Facts(facts...)}})
	if n.Key.Rule == RuleProcess {
		if sec, ok := processSection(d.Proc); ok {
			doc.Sections = append(doc.Sections, sec)
		}
	}
	doc.Sections = append(doc.Sections, serverSection(d))
	return doc
}

// processSection describes a process; the second result is false if it was
// never seen.
func processSection(p *collector.Process) (mailhtml.Section, bool) {
	if p == nil {
		return mailhtml.Section{}, false
	}
	facts := []mailhtml.Fact{
		{Label: "Name", Value: p.Name},
		{Label: "PID", Value: fmt.Sprint(p.PID), Mono: true},
		{Label: "Command", Value: orDash(p.Cmdline), Mono: true},
		{Label: "User", Value: orDash(p.User)},
		{Label: "Origin", Value: DescribeOrigin(p.Origin)},
	}
	if p.Origin.Kind == collector.KindContainer && p.Origin.ContainerName != "" {
		facts = append(facts, mailhtml.Fact{Label: "Check", Value: "docker logs " + p.Origin.ContainerName, Mono: true})
	}
	return mailhtml.Section{Heading: "Process", Blocks: []mailhtml.Block{mailhtml.Facts(facts...)}}, true
}

// serverSection is the name, time, total CPU and load of the server.
func serverSection(d Details) mailhtml.Section {
	facts := []mailhtml.Fact{
		{Label: "Name", Value: d.Server},
		{Label: "Time", Value: d.Now.UTC().Format(factTimeFormat), Mono: true},
	}
	if s := d.Snap; s != nil {
		cpu := "-"
		if s.CPUKnown {
			cpu = format.Percent(s.CPUPercent)
		}
		facts = append(facts,
			mailhtml.Fact{Label: "Total CPU", Value: cpu, Mono: true},
			mailhtml.Fact{Label: "Load", Value: fmt.Sprintf("%.2f %.2f %.2f (1, 5, 15 min)", s.Load1, s.Load5, s.Load15), Mono: true},
		)
	}
	return mailhtml.Section{Heading: "Server", Blocks: []mailhtml.Block{mailhtml.Facts(facts...)}}
}

// topProcessesSection lists the five busiest processes.
func topProcessesSection(heading string, s *collector.Snapshot) mailhtml.Section {
	sec := mailhtml.Section{Heading: heading}
	var top []collector.Process
	if s != nil {
		top = collector.Top(collector.UserProcesses(s.Processes), 5)
	}
	if len(top) == 0 {
		sec.Blocks = []mailhtml.Block{mailhtml.Note("no data")}
		return sec
	}
	rows := make([][]mailhtml.Cell, 0, len(top))
	for _, p := range top {
		cpu := "-"
		if p.CPUKnown {
			cpu = format.Percent(p.CPU)
		}
		rows = append(rows, []mailhtml.Cell{
			{Text: fmt.Sprint(p.PID), Mono: true},
			{Text: p.Name, Sub: p.Origin.String()},
			{Text: cpu, Mono: true},
		})
	}
	sec.Blocks = []mailhtml.Block{
		mailhtml.TableOf([]mailhtml.Col{{Title: "PID"}, {Title: "Process"}, {Title: "CPU", Right: true}}, rows),
		mailhtml.Note("100% is one core."),
	}
	if s.DockerErr != nil {
		sec.Blocks = append(sec.Blocks, mailhtml.Note("Docker was not reachable, so container names may be missing."))
	}
	return sec
}

func diskAlertDoc(n Notice, d Details) mailhtml.Doc {
	mount := n.Key.Mount
	doc := mailhtml.New(d.Server, d.Now)
	doc.Badge = mailhtml.Badge{Level: mailhtml.LevelBad, Text: "Disk alert"}
	doc.Title = fmt.Sprintf("Disk %s at %s", mount, format.Percent(n.Avg))
	if d.Mount != nil {
		doc.Title += fmt.Sprintf(" (%s free)", format.Bytes(d.Mount.Total-d.Mount.Used))
	}
	doc.Lead = fmt.Sprintf("The filesystem mounted at %s is %s full. Threshold: %s. You will get one all-clear mail when this is over.",
		mount, format.Percent(n.Avg), format.Percent(n.Threshold))

	meter := mailhtml.Meter{Label: mount, Percent: n.Avg, Fill: mailhtml.MeterFill(n.Avg, n.Threshold)}
	if m := d.Mount; m != nil {
		meter.Percent = m.UsedPercent
		meter.Fill = mailhtml.MeterFill(m.UsedPercent, n.Threshold)
		meter.Detail = fmt.Sprintf("%s of %s, %s free", format.Bytes(m.Used), format.Bytes(m.Total), format.Bytes(m.Total-m.Used))
	}
	doc.Sections = append(doc.Sections, mailhtml.Section{Heading: "Mount", Blocks: []mailhtml.Block{
		mailhtml.Meters(meter),
		mailhtml.Facts(mailhtml.Fact{Label: "Alert threshold", Value: format.Percent(n.Threshold), Mono: true}),
	}})
	if sec, ok := consumersSection(consumersFor(d, mount)); ok {
		doc.Sections = append(doc.Sections, sec)
	}
	doc.Sections = append(doc.Sections, serverSection(d))
	return doc
}

func diskClearDoc(n Notice, d Details) mailhtml.Doc {
	mount := n.Key.Mount
	doc := mailhtml.New(d.Server, d.Now)
	doc.Badge = mailhtml.Badge{Level: mailhtml.LevelOK, Text: "All clear"}

	facts := []mailhtml.Fact{{Label: "Mount", Value: mount, Mono: true}}
	if n.Reason == ReasonGone {
		doc.Title = fmt.Sprintf("Disk %s is no longer mounted", mount)
		facts = append(facts, mailhtml.Fact{Label: "Why", Value: "The filesystem is no longer mounted, so its disk alert is closed.", Tone: mailhtml.ToneDown})
	} else {
		doc.Title = fmt.Sprintf("Disk %s back to %s", mount, format.Percent(n.Avg))
		facts = append(facts,
			mailhtml.Fact{Label: "Now", Value: format.Percent(n.Avg) + " used", Mono: true, Tone: mailhtml.ToneDown},
			mailhtml.Fact{Label: "Alert threshold", Value: format.Percent(n.Threshold), Mono: true})
	}
	if n.Lasted > 0 {
		facts = append(facts, mailhtml.Fact{Label: "Lasted", Value: "about " + duration(n.Lasted) + " after the alert", Mono: true})
	}
	blocks := []mailhtml.Block{mailhtml.Facts(facts...)}
	if m := d.Mount; m != nil {
		blocks = append([]mailhtml.Block{mailhtml.Meters(mailhtml.Meter{
			Label: mount, Percent: m.UsedPercent, Fill: mailhtml.MeterFill(m.UsedPercent, n.Threshold),
			Detail: fmt.Sprintf("%s of %s, %s free", format.Bytes(m.Used), format.Bytes(m.Total), format.Bytes(m.Total-m.Used)),
		})}, blocks...)
	}
	doc.Sections = append(doc.Sections, mailhtml.Section{Heading: "What happened", Blocks: blocks}, serverSection(d))
	return doc
}

// consumersSection shows what Docker uses on a mount, from the same figures as
// the text mail. The second result is false if there is nothing to show.
func consumersSection(c consumers) (mailhtml.Section, bool) {
	switch c.State {
	case csNone:
		return mailhtml.Section{}, false
	case csUnavailable:
		return mailhtml.Section{Heading: "Docker disk usage", Blocks: []mailhtml.Block{
			mailhtml.Note(fmt.Sprintf("Docker disk usage is not available: %v", c.Err))}}, true
	case csNotMeasured:
		return mailhtml.Section{Heading: "Docker disk usage", Blocks: []mailhtml.Block{
			mailhtml.Note("Docker disk usage has not been measured yet.")}}, true
	}

	u := c.Usage
	sec := mailhtml.Section{Heading: "Biggest consumers (Docker)"}
	if c.OnDockerMount {
		rows := make([][]mailhtml.Cell, 0, len(c.Projects))
		for _, p := range c.Projects {
			rows = append(rows, []mailhtml.Cell{
				{Text: projectName(p), Sub: strings.Join([]string{
					"layers " + format.Bytes(uint64(p.Layers)),
					"volumes " + format.BytesPartial(p.Volumes, p.VolumesUnknown),
					"files " + format.BytesPartial(p.Files, p.FilesUnknown),
					"binds " + format.BytesPartial(p.Binds, p.BindsUnknown),
					"logs " + format.BytesPartial(p.Logs, p.LogsUnknown),
				}, " · ")},
				{Text: format.BytesPartial(p.Total(), !p.Complete()), Mono: true},
			})
		}
		if len(rows) == 0 {
			sec.Blocks = append(sec.Blocks, mailhtml.Note("no containers"))
		} else {
			sec.Blocks = append(sec.Blocks, mailhtml.TableOf([]mailhtml.Col{{Title: "Project"}, {Title: "Size", Right: true}}, rows))
		}
		sec.Blocks = append(sec.Blocks, mailhtml.Facts(
			mailhtml.Fact{Label: "Images", Value: format.Bytes(uint64(u.Images)), Mono: true},
			mailhtml.Fact{Label: "Build cache", Value: format.Bytes(uint64(u.BuildCache)), Mono: true},
			mailhtml.Fact{Label: "Reclaimable", Value: fmt.Sprintf("%s (unused volumes %s + dangling images %s)",
				format.Bytes(uint64(u.Reclaimable)), format.Bytes(uint64(u.UnusedVolumes)), format.Bytes(uint64(u.DanglingImg))), Mono: true},
		))
		if c.CrossMount {
			sec.Blocks = append(sec.Blocks, mailhtml.Note("Project figures include folders and bind mounts on other mounts; those are not subtracted below."))
		}
	} else {
		var rows [][]mailhtml.Cell
		for i, dir := range c.Dirs {
			if i == topProjects {
				break
			}
			rows = append(rows, []mailhtml.Cell{
				{Text: dir.Path, Sub: dir.Label, Mono: true},
				{Text: format.BytesPartial(dir.Bytes, !dir.Known), Mono: true},
			})
		}
		sec.Blocks = append(sec.Blocks,
			mailhtml.Note("Project folders and bind mounts on this mount:"),
			mailhtml.TableOf([]mailhtml.Col{{Title: "Folder"}, {Title: "Size", Right: true}}, rows))
	}

	sec.Blocks = append(sec.Blocks, mailhtml.Facts(mailhtml.Fact{Label: "Other (not Docker)", Value: c.otherText(), Mono: true}))
	if c.Approx {
		sec.Blocks = append(sec.Blocks, mailhtml.Note(`"?" or "+?": part of that could not be read or measured; "~" means the figure is approximate.`))
	}
	return sec, true
}
