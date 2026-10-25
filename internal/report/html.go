package report

import (
	"fmt"
	"sort"
	"strings"

	"eternalshell/internal/format"
	"eternalshell/internal/mailhtml"
)

// RenderHTML makes the HTML part of the report. It shows the same facts as the
// text (Render) and uses the same helpers, so the two cannot drift far apart.
// It returns "" if the page could not be drawn; the plain text is then enough.
func RenderHTML(d Data) string {
	items := d.attention()

	doc := mailhtml.New(d.Name, d.Now)
	doc.Title = d.Name + " status report"
	doc.Lead = d.Now.Format(timeFormat) + " - " + d.period()
	switch len(items) {
	case 0:
		doc.Badge = mailhtml.Badge{Level: mailhtml.LevelOK, Text: "All OK"}
	case 1:
		doc.Badge = mailhtml.Badge{Level: mailhtml.LevelWarn, Text: "1 thing needs attention"}
	default:
		doc.Badge = mailhtml.Badge{Level: mailhtml.LevelWarn, Text: fmt.Sprintf("%d things need attention", len(items))}
	}

	if len(items) > 0 {
		list := make([]mailhtml.Item, len(items))
		for i, it := range items {
			list[i] = mailhtml.Item{Text: it.text, Level: mailhtml.LevelWarn}
		}
		doc.Sections = append(doc.Sections, mailhtml.Section{Heading: "Needs attention", Blocks: []mailhtml.Block{mailhtml.List(list...)}})
	}
	doc.Sections = append(doc.Sections, d.htmlAlerts(), d.htmlDisk())
	doc.Sections = append(doc.Sections, d.htmlProjects()...)
	doc.Sections = append(doc.Sections, d.htmlContainers(), d.htmlCPU(), d.htmlUptime())

	return mailhtml.Render(doc)
}

// changeCell / changeTone turn a change in used bytes into text and a color:
// growth is red, shrinking green, and a change below the noise level is grey.
func changeParts(delta int64) (string, mailhtml.Tone) {
	text := changeText(delta)
	abs := delta
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs < noiseBytes:
		return text, mailhtml.ToneMuted
	case delta > 0:
		return text, mailhtml.ToneUp
	}
	return text, mailhtml.ToneDown
}

func (d Data) htmlAlerts() mailhtml.Section {
	if !d.Watcher {
		return mailhtml.Section{Heading: "Alerts in the period", Blocks: []mailhtml.Block{
			mailhtml.Note("n/a (collected only by the running watcher)"),
		}}
	}
	sec := mailhtml.Section{Heading: "Alerts in the period (since " + d.watchSince().Format(timeFormat) + ")"}
	if len(d.Incidents) == 0 {
		sec.Blocks = []mailhtml.Block{mailhtml.Text("none")}
		return sec
	}
	list := make([]mailhtml.Item, len(d.Incidents))
	for i, inc := range d.Incidents {
		level := mailhtml.LevelOK
		if inc.Active() {
			level = mailhtml.LevelBad
		}
		list[i] = mailhtml.Item{Text: incidentText(inc), Level: level}
	}
	sec.Blocks = []mailhtml.Block{mailhtml.List(list...)}
	return sec
}

func (d Data) htmlDisk() mailhtml.Section {
	sec := mailhtml.Section{Heading: title("Disk", d.Diff.HasPrev)}
	if len(d.Mounts) == 0 {
		sec.Blocks = append(sec.Blocks, mailhtml.Note("no mounts could be read"))
	}
	var meters []mailhtml.Meter
	for _, m := range d.Mounts {
		meter := mailhtml.Meter{
			Label:   m.Path,
			Detail:  fmt.Sprintf("%s of %s", format.Bytes(m.Used), format.Bytes(m.Total)),
			Percent: m.UsedPercent,
			Fill:    mailhtml.MeterFill(m.UsedPercent, d.DiskAlertPercent),
		}
		if d.Diff.HasPrev {
			isNew := false
			for _, p := range d.Diff.NewMounts {
				isNew = isNew || p == m.Path
			}
			if isNew {
				meter.Change, meter.ChangeTone = "new mount", mailhtml.ToneMuted
			} else if delta, ok := d.Diff.MountUsed[m.Path]; ok {
				meter.Change, meter.ChangeTone = changeParts(delta)
			}
		}
		meters = append(meters, meter)
	}
	if len(meters) > 0 {
		sec.Blocks = append(sec.Blocks, mailhtml.Meters(meters...))
	}
	for _, p := range d.Diff.GoneMounts {
		sec.Blocks = append(sec.Blocks, mailhtml.Note(p+" is no longer mounted (it was at the previous report)"))
	}
	return sec
}

// htmlProjects returns the card with the biggest projects and the one with the
// reclaimable space (or a single card saying why there are none).
func (d Data) htmlProjects() []mailhtml.Section {
	heading := title("Top projects by disk use", d.Diff.DiskCompared)
	u := d.Disk
	if u == nil {
		note := "not measured yet"
		if d.DiskErr != nil {
			note = fmt.Sprintf("not available: %v", d.DiskErr)
		}
		return []mailhtml.Section{{Heading: heading, Blocks: []mailhtml.Block{mailhtml.Note(note)}}}
	}

	var rows [][]mailhtml.Cell
	for _, p := range u.Projects {
		if len(rows) == topProjects || p.Total() == 0 {
			break
		}
		key, name := projectKey(p.Name), p.Name
		if p.Standalone {
			key, name = containerKey(p.Name), p.Name+" (container)"
		}
		change := mailhtml.Cell{Mono: true}
		if d.Diff.DiskCompared {
			for _, k := range d.Diff.NewProjects {
				if k == key {
					change = mailhtml.Cell{Text: "new", Tone: mailhtml.ToneMuted, Mono: true}
				}
			}
			if delta, ok := d.Diff.ProjectDelta[key]; ok {
				text, tone := changeParts(delta)
				change = mailhtml.Cell{Text: text, Tone: tone, Mono: true}
			}
		}
		rows = append(rows, []mailhtml.Cell{
			{Text: name},
			{Text: format.BytesPartial(p.Total(), !p.Complete()), Mono: true},
			change,
		})
	}
	projects := mailhtml.Section{Heading: heading}
	if len(rows) == 0 {
		projects.Blocks = []mailhtml.Block{mailhtml.Note("no containers")}
	} else {
		projects.Blocks = []mailhtml.Block{mailhtml.TableOf(
			[]mailhtml.Col{{Title: "Project"}, {Title: "Size", Right: true}, {Title: "Change", Right: true}}, rows)}
	}

	value := fmt.Sprintf("%s (unused volumes %s + dangling images %s)",
		format.Bytes(uint64(u.Reclaimable)), format.Bytes(uint64(u.UnusedVolumes)), format.Bytes(uint64(u.DanglingImg)))
	fact := mailhtml.Fact{Label: "Can be freed", Value: value, Mono: true}
	reclaim := mailhtml.Section{Heading: title("Reclaimable", d.Diff.DiskCompared), Blocks: []mailhtml.Block{mailhtml.Facts(fact)}}
	if d.Diff.DiskCompared {
		text, tone := changeParts(d.Diff.ReclaimDelta)
		reclaim.Blocks = []mailhtml.Block{mailhtml.Facts(fact, mailhtml.Fact{Label: "Change", Value: text, Mono: true, Tone: tone})}
	}
	return []mailhtml.Section{projects, reclaim}
}

func (d Data) htmlContainers() mailhtml.Section {
	sec := mailhtml.Section{Heading: "Containers"}
	if len(d.Containers) == 0 {
		sec.Blocks = []mailhtml.Block{mailhtml.Note("none (or Docker is not reachable)")}
		return sec
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

	rows := make([][]mailhtml.Cell, 0, len(names))
	for _, g := range names {
		c := groups[g]
		stopped := "-"
		if c.total > c.running {
			stopped = fmt.Sprint(c.total - c.running)
		}
		rows = append(rows, []mailhtml.Cell{{Text: g}, {Text: fmt.Sprint(c.running), Mono: true}, {Text: stopped, Mono: true}})
	}
	sec.Blocks = []mailhtml.Block{mailhtml.TableOf(
		[]mailhtml.Col{{Title: "Project"}, {Title: "Running", Right: true}, {Title: "Stopped", Right: true}}, rows)}

	if ch := d.Diff; ch.HasPrev {
		if added := append(append([]string{}, ch.AddedGroups...), ch.AddedContainers...); len(added) > 0 {
			sec.Blocks = append(sec.Blocks, mailhtml.Note("New since the previous report: "+strings.Join(added, ", ")))
		}
		if removed := append(append([]string{}, ch.RemovedGroups...), ch.RemovedContainers...); len(removed) > 0 {
			sec.Blocks = append(sec.Blocks, mailhtml.Note("Removed since the previous report: "+strings.Join(removed, ", ")))
		}
	}
	return sec
}

func (d Data) htmlCPU() mailhtml.Section {
	switch {
	case !d.Watcher:
		return mailhtml.Section{Heading: "CPU", Blocks: []mailhtml.Block{mailhtml.Note("n/a (collected only by the running watcher)")}}
	case d.CPU == nil:
		return mailhtml.Section{Heading: "CPU", Blocks: []mailhtml.Block{mailhtml.Note("no samples yet")}}
	}
	return mailhtml.Section{Heading: "CPU (since " + d.watchSince().Format(timeFormat) + ")", Blocks: []mailhtml.Block{
		mailhtml.Facts(
			mailhtml.Fact{Label: "Average", Value: format.Percent(d.CPU.Avg), Mono: true},
			mailhtml.Fact{Label: "Peak", Value: fmt.Sprintf("%s at %s", format.Percent(d.CPU.Peak), d.CPU.PeakAt.Format("Mon 15:04")), Mono: true},
		),
		mailhtml.Note("100% is the whole machine."),
	}}
}

func (d Data) htmlUptime() mailhtml.Section {
	facts := []mailhtml.Fact{{Label: "Up", Value: duration(d.Uptime), Mono: true}}
	switch {
	case !d.Diff.HasPrev:
	case d.Diff.Rebooted:
		facts = append(facts, mailhtml.Fact{Label: "Reboot", Value: "yes, booted at " + d.BootTime.Format(timeFormat), Tone: mailhtml.ToneUp})
	default:
		facts = append(facts, mailhtml.Fact{Label: "Reboot", Value: "none since the previous report", Tone: mailhtml.ToneMuted})
	}
	return mailhtml.Section{Heading: "Uptime", Blocks: []mailhtml.Block{mailhtml.Facts(facts...)}}
}
