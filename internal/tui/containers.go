package tui

import (
	"github.com/charmbracelet/bubbles/table"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

const standaloneGroup = "standalone"

// Fixed column widths (content only; the table adds 2 columns of padding each).
const (
	statusW   = 26
	cpuW      = 6
	memW      = 8
	restartsW = 8
	cellPad   = 2
)

func containerColumns(width int) []table.Column {
	fixed := statusW + cpuW + memW + restartsW + 5*cellPad
	return []table.Column{
		{Title: "PROJECT / CONTAINER", Width: max(width-fixed, 20)},
		{Title: "STATUS", Width: statusW},
		{Title: "CPU", Width: cpuW},
		{Title: "MEM", Width: memW},
		{Title: "RESTARTS", Width: restartsW},
	}
}

// containerRows builds the table rows: a header row per compose project, then
// its containers. cs must already be sorted by project (collector does that).
func containerRows(cs []collector.Container) []table.Row {
	var rows []table.Row
	group := "\x00" // matches no real project name
	for _, c := range cs {
		g := c.Project
		if g == "" {
			g = standaloneGroup
		}
		if g != group {
			group = g
			rows = append(rows, table.Row{group, "", "", "", ""})
		}
		rows = append(rows, containerRow(c))
	}
	return rows
}

func containerRow(c collector.Container) table.Row {
	// Inside a compose project the service name is shorter and clearer than
	// the generated container name ("project-service-1").
	name := c.Name
	if c.Project != "" && c.Service != "" {
		name = c.Service
	}

	marker := "○"
	cpu, mem := "-", "-"
	if c.Running() {
		marker = "●"
		cpu = format.Percent(c.CPU)
		if c.MemOK {
			mem = format.Bytes(c.MemBytes)
		}
	}
	return table.Row{"  " + marker + " " + name, c.Status, cpu, mem, itoa(c.Restarts)}
}
