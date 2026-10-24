package tui

import (
	"regexp"
	"sort"
	"strconv"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

const standaloneGroup = "standalone"

// highCPU is the per-core CPU (100 = one core) from which a running container
// counts as a problem. Same default as the process alert in the concept.
const highCPU = 80

// Fixed column widths. The list draws a gap of colGap between columns and one
// space at the left edge.
const (
	statusW   = 26
	cpuW      = 6
	memW      = 8
	restartsW = 8
	colGap    = 2
	cellPad   = 2 // padding the bubbles table adds per column (processes view)
)

func containerColumns(width int) []listColumn {
	fixed := statusW + cpuW + memW + restartsW + 4*colGap + 1
	return []listColumn{
		{"PROJECT / CONTAINER", max(width-fixed, 20)},
		{"STATUS", statusW},
		{"CPU", cpuW},
		{"MEM", memW},
		{"RESTARTS", restartsW},
	}
}

var exitedRe = regexp.MustCompile(`^Exited \((\d+)\)`)

// exitCode extracts the exit code from a Docker status like "Exited (143) 6 months ago".
// ok is false if the status is not an "Exited" status.
func exitCode(status string) (code int, ok bool) {
	m := exitedRe.FindStringSubmatch(status)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

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

// containerRows builds the list rows: a header row per group, then its containers.
func containerRows(cs []collector.Container) []listRow {
	var rows []listRow
	for _, g := range groupContainers(cs) {
		title := g.name
		if g.problem {
			title += " !"
		}
		rows = append(rows, listRow{cells: []string{title}, header: true, warn: g.problem})
		for _, c := range g.containers {
			rows = append(rows, containerRow(c))
		}
	}
	return rows
}

func containerRow(c collector.Container) listRow {
	// Inside a compose project the service name is shorter and clearer than
	// the generated container name ("project-service-1").
	name := c.Name
	if c.Project != "" && c.Service != "" {
		name = c.Service
	} else if c.Image != "" {
		name += " (" + c.Image + ")" // without a project the image says what it is
	}

	marker := "○"
	cpu, mem := "-", "-"
	if c.Running() {
		marker = "●"
		if c.CPUKnown {
			cpu = format.Percent(c.CPU)
		}
		if c.MemOK {
			mem = format.Bytes(c.MemBytes)
		}
	}
	return listRow{
		cells: []string{"  " + marker + " " + name, c.Status, cpu, mem, strconv.Itoa(c.Restarts)},
		dim:   !c.Running(),
		id:    c.ID,
	}
}
