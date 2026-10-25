package tui

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

const standaloneGroup = "standalone"

// highCPU is the per-core CPU (100 = one core) from which a running container
// counts as a problem. Same default as the process alert in the concept.
const highCPU = 80

// Column widths. The list draws a gap of colGap between columns and one space
// at the left edge. cellPad is what the bubbles table adds per column (the
// processes view).
const (
	dotW      = 2
	cpuW      = 5
	memW      = 8
	diskW     = 9
	restartsW = 8
	colGap    = 2
	cellPad   = 2

	// rightMargin is the space kept free at the right edge of a list, so the last
	// column does not touch the border.
	rightMargin = 1
)

// Column indexes of the containers list.
const (
	colDot = iota
	colName
	colStatus
	colCPU
	colTrend
	colMem
	colDisk
	colRestarts
)

// containerLayouts are the widths to try, widest first: whether the trend
// column is shown, how wide STATUS may be and how much room the name needs at
// least. The first one that fits is used, so the trend column goes first when
// space is tight, then STATUS shrinks. The name gets what is left.
var containerLayouts = []struct{ trend, status, nameMin int }{
	{sparkWidth, 26, 24},
	{0, 26, 22},
	{0, 20, 20},
	{0, 14, 18},
	{0, 12, 12},
}

func containerColumns(width int) []listColumn {
	width -= rightMargin
	pick := containerLayouts[len(containerLayouts)-1]
	for _, c := range containerLayouts {
		cols := 7 // dot, name, status, cpu, mem, disk, restarts
		if c.trend > 0 {
			cols++
		}
		need := 1 + (cols-1)*colGap + dotW + cpuW + memW + diskW + restartsW + c.status + c.trend + c.nameMin
		if need <= width {
			pick = c
			break
		}
	}
	cols := 7
	if pick.trend > 0 {
		cols++
	}
	rest := width - (1 + (cols-1)*colGap + dotW + cpuW + memW + diskW + restartsW + pick.status + pick.trend)
	return []listColumn{
		colDot:      {"", dotW},
		colName:     {"PROJECT / CONTAINER", max(rest, 6)},
		colStatus:   {"STATUS", pick.status},
		colCPU:      {"CPU", cpuW},
		colTrend:    {"TREND", pick.trend},
		colMem:      {"MEM", memW},
		colDisk:     {"DISK", diskW},
		colRestarts: {"RESTARTS", restartsW},
	}
}

// exitCode extracts the exit code from a Docker status like "Exited (143) 6 months ago".
func exitCode(status string) (code int, ok bool) { return collector.ExitCode(status) }

// problem reports whether a container deserves attention:
//   - running: restarted before, or using at least a full core's 80 %
//   - exited: with an exit code other than 0 or 143 (143 = SIGTERM, which is
//     what a normal `docker stop` produces)
//   - dead or restarting
//
// Restarts of a container that is stopped now do not count; it is not running
// anyway, and its state already says what is needed.
func problem(c collector.Container) bool {
	switch {
	case c.Running():
		return c.Restarts > 0 || c.CPU >= highCPU
	case c.State == "exited":
		code, ok := exitCode(c.Status)
		return !ok || (code != 0 && code != 143)
	case c.State == "dead", c.State == "restarting":
		return true
	}
	return false
}

// group is one compose project (or "standalone") with its containers.
type group struct {
	name       string
	containers []collector.Container
	problem    bool
}

// groupContainers splits containers into groups and orders them: groups with a
// problem first, then alphabetical, "standalone" last among equals. Inside a
// group, problem containers come first, then by name.
func groupContainers(cs []collector.Container) []group {
	byName := map[string]*group{}
	var groups []*group
	for _, c := range cs {
		name := c.Project
		if name == "" {
			name = standaloneGroup
		}
		g, ok := byName[name]
		if !ok {
			g = &group{name: name}
			byName[name] = g
			groups = append(groups, g)
		}
		g.containers = append(g.containers, c)
		g.problem = g.problem || problem(c)
	}

	for _, g := range groups {
		sort.SliceStable(g.containers, func(i, j int) bool {
			a, b := g.containers[i], g.containers[j]
			if pa, pb := problem(a), problem(b); pa != pb {
				return pa
			}
			return a.Name < b.Name
		})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.problem != b.problem {
			return a.problem
		}
		if (a.name == standaloneGroup) != (b.name == standaloneGroup) {
			return b.name == standaloneGroup
		}
		return a.name < b.name
	})

	out := make([]group, len(groups))
	for i, g := range groups {
		out[i] = *g
	}
	return out
}

// containerRows builds the list rows: a header row per group, then its
// containers. disk is the latest Docker disk usage (nil until the first
// background result, then the DISK cells show "…" or "-" if it failed). hist is
// the recent CPU of each container, for the trend column.
func containerRows(cs []collector.Container, disk *collector.DiskUsage, diskErr error, hist map[string][]float64) []listRow {
	var rows []listRow
	for _, g := range groupContainers(cs) {
		// The project row: name, how many containers it has, and the sum of
		// their trends. A problem somewhere inside is marked and turns it red.
		title := fmt.Sprintf("%s (%d)", g.name, len(g.containers))
		if g.problem {
			title += " !"
		}
		series := make([][]float64, 0, len(g.containers))
		for _, c := range g.containers {
			series = append(series, hist[c.ID])
		}
		head := make([]string, colRestarts+1)
		head[colName] = title
		head[colTrend] = trendCell(sumSeries(series...))
		head[colDisk] = groupDiskCell(g, disk, diskErr)
		rows = append(rows, listRow{cells: head, header: true, warn: g.problem})

		for _, c := range g.containers {
			rows = append(rows, containerRow(c, diskCell(c.ID, disk, diskErr), hist[c.ID]))
		}
	}
	return rows
}

// diskCell is the DISK text of one container: layers + volumes + bind mounts +
// logs. "+?" marks a figure that is incomplete (for example the log files of a
// normal user, who cannot read /var/lib/docker).
func diskCell(id string, disk *collector.DiskUsage, diskErr error) string {
	if disk == nil {
		if diskErr != nil {
			return "-"
		}
		return "…"
	}
	p, ok := disk.ByContainer[id]
	if !ok {
		return "…" // started after the last refresh
	}
	return format.BytesPartial(p.Total(), !p.Complete())
}

// groupDiskCell is the DISK text of a project row: its containers plus the
// project folder. The folder belongs to the project, not to one container, so
// a project row can be bigger than the sum of the rows below it.
func groupDiskCell(g group, disk *collector.DiskUsage, diskErr error) string {
	if disk == nil {
		return diskCell("", nil, diskErr)
	}
	var total int64
	unknown := false
	for _, c := range g.containers {
		p, ok := disk.ByContainer[c.ID]
		if !ok {
			return "…"
		}
		total += p.Total()
		unknown = unknown || !p.Complete()
	}
	if len(g.containers) > 0 && g.containers[0].Project != "" {
		files, filesUnknown := disk.FilesOf(g.containers[0].Project)
		total += files
		unknown = unknown || filesUnknown
	}
	return format.BytesPartial(total, unknown)
}

// dotKind is what the status dot of a container says.
type dotKind int

const (
	dotStopped dotKind = iota // stopped cleanly or only created: grey
	dotRunning                // running: green (with a check mark when healthy)
	dotWarn                   // unhealthy or restarting: yellow
	dotBad                    // exited badly or dead: red
)

// health reads the health state out of a Docker status like "Up 2 weeks (healthy)".
func health(status string) (healthy, unhealthy bool) { return collector.Health(status) }

// containerDot decides the kind of dot for a container and whether it gets a
// check mark. "Exited badly" is the same rule as for the problem marker: an exit
// code other than 0 or 143.
func containerDot(c collector.Container) (kind dotKind, healthy bool) {
	h, unhealthy := health(c.Status)
	switch {
	case c.Running() && unhealthy:
		return dotWarn, false
	case c.Running():
		return dotRunning, h
	case c.State == "restarting":
		return dotWarn, false
	case c.State == "dead":
		return dotBad, false
	case c.State == "exited":
		code, ok := exitCode(c.Status)
		if !ok || (code != 0 && code != 143) {
			return dotBad, false
		}
	}
	return dotStopped, false
}

// dotCell returns the text of the dot column and its style. In plain mode (no
// colors) the shapes differ instead: ● running, ◐ unhealthy or restarting,
// ✕ exited badly or dead, ○ stopped.
func dotCell(c collector.Container) (string, *lipgloss.Style) {
	kind, healthy := containerDot(c)
	plain := plainMode()

	glyph, style := "●", &greenCell
	switch kind {
	case dotWarn:
		style = &yellowCell
		if plain {
			glyph = "◐"
		}
	case dotBad:
		style = &redCell
		if plain {
			glyph = "✕"
		}
	case dotStopped:
		glyph, style = "○", &dimStyle
	}
	mark := " "
	if healthy {
		mark = "✓"
	}
	return glyph + mark, style
}

func containerRow(c collector.Container, disk string, history []float64) listRow {
	// Inside a compose project the service name is shorter and clearer than
	// the generated container name ("project-service-1").
	name := c.Name
	if c.Project != "" && c.Service != "" {
		name = c.Service
	} else if c.Image != "" {
		name += " (" + c.Image + ")" // without a project the image says what it is
	}

	cpu, mem, trend := "-", "-", ""
	if c.Running() {
		if c.CPUKnown {
			cpu = format.Percent(c.CPU)
		}
		if c.MemOK {
			mem = format.Bytes(c.MemBytes)
		}
		trend = trendCell(history)
	}

	dot, dotStyle := dotCell(c)
	cells := make([]string, colRestarts+1)
	cells[colDot], cells[colName], cells[colStatus] = dot, name, c.Status
	cells[colCPU], cells[colTrend], cells[colMem] = cpu, trend, mem
	cells[colDisk], cells[colRestarts] = disk, strconv.Itoa(c.Restarts)

	styles := make([]*lipgloss.Style, colRestarts+1)
	styles[colDot] = dotStyle
	if c.Running() {
		styles[colTrend] = &accentStyle
	}
	return listRow{cells: cells, styles: styles, dim: !c.Running(), id: c.ID}
}
