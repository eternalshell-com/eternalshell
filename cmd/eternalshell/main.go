// Command eternalshell shows the state of the server.
package main

import (
	"fmt"
	"os"
	"time"

	"eternalshell/internal/collector"
)

const usage = `Usage: eternalshell <command>

Commands:
  status    print CPU, load, memory, disks and the top 5 processes
`

func main() {
	if len(os.Args) != 2 || os.Args[1] != "status" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := runStatus(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
	fmt.Printf("MEM     %.0f %% (%s of %s)\n", snap.MemPercent, human(snap.MemUsed), human(snap.MemTotal))
	for _, m := range snap.Mounts {
		fmt.Printf("DISK    %s %.0f %% (%s of %s)\n", m.Path, m.UsedPercent, human(m.Used), human(m.Total))
	}

	fmt.Println("\nPROCESSES (top 5 by CPU, 100 % = one core)")
	fmt.Printf("%-8s %-20s %7s %8s  %s\n", "PID", "NAME", "CPU", "MEM", "ORIGIN")
	for _, p := range collector.Top(snap.Processes, 5) {
		fmt.Printf("%-8d %-20.20s %6.0f%% %8s  %s\n", p.PID, p.Name, p.CPU, human(p.RSS), p.Origin)
	}
	return nil
}

// human formats bytes as a short size, e.g. 2.6G or 90M.
func human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(b)/float64(div), "KMGTPE"[exp])
}
