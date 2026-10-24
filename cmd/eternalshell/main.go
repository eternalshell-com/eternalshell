// Command eternalshell shows the state of the server.
package main

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
	"eternalshell/internal/tui"
)

const usage = `Usage: eternalshell [command]

Without a command, eternalshell opens the live terminal UI.

Commands:
  status    print CPU, load, memory, disks and the top 5 processes
`

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = runTUI()
	case len(os.Args) == 2 && os.Args[1] == "status":
		err = runStatus()
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runTUI opens the full-screen UI and blocks until the user quits.
func runTUI() error {
	c := collector.New()
	defer c.Close()
	_, err := tea.NewProgram(tui.New(c), tea.WithAltScreen()).Run()
	return err
}

func runStatus() error {
	c := collector.New()
	defer c.Close()
	snap, err := c.Collect(time.Second)
	if err != nil {
		return err
	}

	fmt.Printf("CPU     %.1f %% total\n", snap.CPUPercent)
	fmt.Printf("LOAD    %.2f %.2f %.2f\n", snap.Load1, snap.Load5, snap.Load15)
	fmt.Printf("MEM     %.0f %% (%s of %s)\n", snap.MemPercent, format.Bytes(snap.MemUsed), format.Bytes(snap.MemTotal))
	for _, m := range snap.Mounts {
		fmt.Printf("DISK    %s %.0f %% (%s of %s)\n", m.Path, m.UsedPercent, format.Bytes(m.Used), format.Bytes(m.Total))
	}

	fmt.Println("\nPROCESSES (top 5 by CPU, 100 % = one core)")
	fmt.Printf("%-8s %-20s %7s %8s  %s\n", "PID", "NAME", "CPU", "MEM", "ORIGIN")
	for _, p := range collector.Top(collector.UserProcesses(snap.Processes), 5) {
		fmt.Printf("%-8d %-20.20s %7s %8s  %s\n", p.PID, p.Name, format.Percent(p.CPU), format.Bytes(p.RSS), p.Origin)
	}
	return nil
}
