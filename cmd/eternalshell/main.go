// Command eternalshell zeigt den Zustand des Servers an.
package main

import (
	"fmt"
	"os"
	"time"

	"eternalshell/internal/collector"
)

const usage = `Aufruf: eternalshell <befehl>

Befehle:
  status    CPU, Load, RAM, Disk und die Top-5-Prozesse ausgeben
`

func main() {
	if len(os.Args) != 2 || os.Args[1] != "status" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := runStatus(); err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func runStatus() error {
	snap, err := collector.Collect(time.Second)
	if err != nil {
		return err
	}

	fmt.Printf("CPU     %.1f %% gesamt\n", snap.CPUPercent)
	fmt.Printf("LOAD    %.2f %.2f %.2f\n", snap.Load1, snap.Load5, snap.Load15)
	fmt.Printf("RAM     %.0f %% (%s von %s)\n", snap.MemPercent, human(snap.MemUsed), human(snap.MemTotal))
	for _, m := range snap.Mounts {
		fmt.Printf("DISK    %s %.0f %% (%s von %s)\n", m.Path, m.UsedPercent, human(m.Used), human(m.Total))
	}

	fmt.Println("\nPROZESSE (Top 5 nach CPU, 100 % = ein Kern)")
	fmt.Printf("%-8s %-20s %7s %8s\n", "PID", "NAME", "CPU", "RAM")
	for _, p := range collector.Top(snap.Processes, 5) {
		fmt.Printf("%-8d %-20.20s %6.0f%% %8s\n", p.PID, p.Name, p.CPU, human(p.RSS))
	}
	return nil
}

// human formatiert Bytes als kurze Größe, z. B. 2.6G oder 90M.
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
