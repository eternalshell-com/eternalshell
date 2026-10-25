package tui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/table"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

func itoa(n int) string { return strconv.Itoa(n) }

// procKey identifies one process: a reused PID has a different start time.
type procKey struct {
	pid   int32
	start uint64
}

const (
	procPIDW   = 8
	procNameW  = 24
	procCPUW   = 7
	procMemW   = 9
	procOrigin = 20 // least room for ORIGIN
)

// withTrend reports whether the trend column fits next to the others: it needs
// the trend itself, more room for ORIGIN, and the table padding of a column.
func withTrend(width int) bool {
	fixed := procPIDW + procNameW + procCPUW + procMemW + sparkWidth + 6*cellPad
	return width-fixed >= procOrigin+4
}

func processColumns(width int) []table.Column {
	trend := withTrend(width)
	cols := []table.Column{
		{Title: "PID", Width: procPIDW},
		{Title: "PROCESS", Width: procNameW},
		{Title: "CPU", Width: procCPUW},
	}
	n := 5
	fixed := procPIDW + procNameW + procCPUW + procMemW
	if trend {
		cols = append(cols, table.Column{Title: "TREND", Width: sparkWidth})
		fixed += sparkWidth
		n++
	}
	cols = append(cols,
		table.Column{Title: "MEM", Width: procMemW},
		table.Column{Title: "ORIGIN", Width: max(width-fixed-n*cellPad, procOrigin)},
	)
	return cols
}

// processRows builds one row per process. ps is already sorted by CPU.
// Kernel threads are left out unless showKernel is set. The rows have a trend
// cell exactly when the columns have a trend column (see withTrend).
func processRows(ps []collector.Process, showKernel bool, hist map[procKey][]float64, trend bool) []table.Row {
	rows := make([]table.Row, 0, len(ps))
	for _, p := range ps {
		if p.Kernel && !showKernel {
			continue
		}
		cpu := "-"
		if p.CPUKnown {
			cpu = format.Percent(p.CPU)
		}
		row := table.Row{strconv.Itoa(int(p.PID)), p.Name, cpu}
		if trend {
			row = append(row, trendCell(hist[procKey{p.PID, p.Start}]))
		}
		row = append(row, format.Bytes(p.RSS), p.Origin.String())
		rows = append(rows, row)
	}
	return rows
}
