package tui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/table"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

func itoa(n int) string { return strconv.Itoa(n) }

func processColumns(width int) []table.Column {
	const pidW, nameW, procCPUW, procMemW = 8, 24, 7, 9
	fixed := pidW + nameW + procCPUW + procMemW + 5*cellPad
	return []table.Column{
		{Title: "PID", Width: pidW},
		{Title: "PROCESS", Width: nameW},
		{Title: "CPU", Width: procCPUW},
		{Title: "MEM", Width: procMemW},
		{Title: "ORIGIN", Width: max(width-fixed, 20)},
	}
}

// processRows builds one row per process. ps is already sorted by CPU.
// Kernel threads are left out unless showKernel is set.
func processRows(ps []collector.Process, showKernel bool) []table.Row {
	rows := make([]table.Row, 0, len(ps))
	for _, p := range ps {
		if p.Kernel && !showKernel {
			continue
		}
		cpu := "-"
		if p.CPUKnown {
			cpu = format.Percent(p.CPU)
		}
		rows = append(rows, table.Row{
			strconv.Itoa(int(p.PID)),
			p.Name,
			cpu,
			format.Bytes(p.RSS),
			p.Origin.String(),
		})
	}
	return rows
}
