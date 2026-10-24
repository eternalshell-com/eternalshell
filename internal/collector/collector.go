// Package collector liest Systemwerte (CPU, RAM, Load, Disk, Prozesse).
// Es liest nur, es verändert nichts auf dem Server.
package collector

import (
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// Process beschreibt einen Prozess zum Messzeitpunkt.
// CPU ist pro Kern angegeben wie bei top: 100 = ein Kern voll ausgelastet.
type Process struct {
	PID  int32
	Name string
	CPU  float64
	RSS  uint64 // belegter Arbeitsspeicher in Bytes
}

// Mount beschreibt ein eingehängtes Dateisystem.
type Mount struct {
	Path        string
	Total       uint64
	Used        uint64
	UsedPercent float64
}

// Snapshot ist eine Momentaufnahme des Servers.
type Snapshot struct {
	CPUPercent float64 // gesamter Server, 0-100
	Load1      float64
	Load5      float64
	Load15     float64
	MemTotal   uint64
	MemUsed    uint64
	MemPercent float64
	Mounts     []Mount
	Processes  []Process // alle lesbaren Prozesse, nach CPU absteigend sortiert
}

// Collect misst über das Intervall interval (z. B. 1 s) und gibt den Snapshot zurück.
// CPU-Werte sind Durchschnitte über dieses Intervall, nicht seit Prozessstart.
func Collect(interval time.Duration) (*Snapshot, error) {
	s := &Snapshot{}

	// Erste Messung der Prozess-CPU-Zeiten, dann warten, dann zweite Messung.
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	before := cpuTimes(procs)

	// Gesamt-CPU wartet selbst das Intervall ab; Prozesse warten gleichzeitig mit.
	total, err := cpu.Percent(interval, false)
	if err != nil {
		return nil, err
	}
	if len(total) > 0 {
		s.CPUPercent = total[0]
	}

	s.Processes = measure(procs, before, interval)

	if avg, err := load.Avg(); err == nil {
		s.Load1, s.Load5, s.Load15 = avg.Load1, avg.Load5, avg.Load15
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemTotal, s.MemUsed, s.MemPercent = vm.Total, vm.Used, vm.UsedPercent
	}
	s.Mounts = mounts()
	return s, nil
}

// cpuTimes liest die bisher verbrauchte CPU-Zeit (Sekunden) pro PID.
// Prozesse, die wir nicht lesen dürfen oder die gerade enden, werden übersprungen.
func cpuTimes(procs []*process.Process) map[int32]float64 {
	out := make(map[int32]float64, len(procs))
	for _, p := range procs {
		if t, err := p.Times(); err == nil {
			out[p.Pid] = t.User + t.System
		}
	}
	return out
}

// measure berechnet aus zwei Messungen die CPU pro Prozess und sortiert das Ergebnis.
func measure(procs []*process.Process, before map[int32]float64, interval time.Duration) []Process {
	after := cpuTimes(procs)
	var result []Process
	for _, p := range procs {
		b, ok1 := before[p.Pid]
		a, ok2 := after[p.Pid]
		if !ok1 || !ok2 {
			continue // Prozess war nur in einer Messung da
		}
		name, err := p.Name()
		if err != nil {
			continue
		}
		var rss uint64
		if mi, err := p.MemoryInfo(); err == nil {
			rss = mi.RSS
		}
		result = append(result, Process{
			PID:  p.Pid,
			Name: name,
			CPU:  (a - b) / interval.Seconds() * 100,
			RSS:  rss,
		})
	}
	sortByCPU(result)
	return result
}

// sortByCPU sortiert absteigend nach CPU, bei Gleichstand nach PID (stabile Ausgabe).
func sortByCPU(ps []Process) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].CPU != ps[j].CPU {
			return ps[i].CPU > ps[j].CPU
		}
		return ps[i].PID < ps[j].PID
	})
}

// Top gibt die ersten n Prozesse nach CPU zurück.
func Top(ps []Process, n int) []Process {
	sorted := append([]Process(nil), ps...) // Kopie, damit das Original unverändert bleibt
	sortByCPU(sorted)
	if n < len(sorted) {
		sorted = sorted[:n]
	}
	return sorted
}

// mounts liefert echte Dateisysteme ohne Doppelte (Bind-Mounts) und ohne Snap-Images.
func mounts() []Mount {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Mount
	for _, p := range parts {
		if seen[p.Device] || p.Fstype == "squashfs" {
			continue
		}
		seen[p.Device] = true
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, Mount{Path: p.Mountpoint, Total: u.Total, Used: u.Used, UsedPercent: u.UsedPercent})
	}
	return out
}
