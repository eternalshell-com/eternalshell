// Package collector reads system values (CPU, memory, load, disks, processes).
// It only reads; it never changes anything on the server.
package collector

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// Process describes a process at measurement time.
// CPU is per core like top: 100 means one core fully used.
type Process struct {
	PID  int32
	Name string
	CPU  float64
	RSS  uint64 // resident memory in bytes

	Origin Origin // container, systemd service or host
}

// Mount describes a mounted filesystem.
type Mount struct {
	Path        string
	Total       uint64
	Used        uint64
	UsedPercent float64
}

// Snapshot is a point-in-time view of the server.
type Snapshot struct {
	CPUPercent float64 // whole server, 0-100
	Load1      float64
	Load5      float64
	Load15     float64
	MemTotal   uint64
	MemUsed    uint64
	MemPercent float64
	Mounts     []Mount
	Processes  []Process // all readable processes, sorted by CPU descending

	Containers []Container // empty if Docker is unreachable
	DockerErr  error       // why Docker data is missing; not a failure of the snapshot
}

// Collector takes snapshots. It keeps state between snapshots (the Docker
// client and its restart-count cache), so create it once and reuse it.
type Collector struct {
	docker *Docker
}

// New creates a Collector. It does not connect to Docker yet.
func New() *Collector {
	return &Collector{docker: newDocker()}
}

// Close releases the Docker client.
func (c *Collector) Close() {
	c.docker.close()
}

type dockerResult struct {
	containers []Container
	err        error
}

// Collect measures over the given interval (e.g. 1 s) and returns a snapshot.
// CPU values are averages over this interval, not since process start.
// If Docker is unreachable the snapshot is still returned, with DockerErr set.
func (c *Collector) Collect(interval time.Duration) (*Snapshot, error) {
	s := &Snapshot{}

	// Ask Docker in a goroutine while we wait for the CPU measurement.
	// A buffered channel lets the goroutine finish even if nobody receives.
	dockerCh := make(chan dockerResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		defer cancel()
		cs, err := c.docker.list(ctx)
		dockerCh <- dockerResult{cs, err}
	}()

	// First reading of every process's CPU time, then wait, then second reading.
	procs, err := process.Processes()
	if err != nil {
		return nil, err
	}
	before := cpuTimes(procs)

	// The total CPU measurement sleeps for the interval itself.
	total, err := cpu.Percent(interval, false)
	if err != nil {
		return nil, err
	}
	if len(total) > 0 {
		s.CPUPercent = total[0]
	}

	s.Processes = measure(procs, before, interval)

	d := <-dockerCh // wait for the goroutine's answer
	s.Containers, s.DockerErr = d.containers, d.err
	attach(s.Processes, s.Containers)
	for i := range s.Containers {
		ct := &s.Containers[i]
		if ct.Running() && ct.CgroupPath != "" {
			ct.MemBytes, ct.MemOK = readMemUsage(cgroupBase, ct.CgroupPath)
		}
	}

	if avg, err := load.Avg(); err == nil {
		s.Load1, s.Load5, s.Load15 = avg.Load1, avg.Load5, avg.Load15
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		s.MemTotal, s.MemUsed, s.MemPercent = vm.Total, vm.Used, vm.UsedPercent
	}
	s.Mounts = mounts()
	return s, nil
}

// cpuTimes reads the CPU time (seconds) each PID has used so far.
// Processes we may not read, or that are exiting, are skipped.
func cpuTimes(procs []*process.Process) map[int32]float64 {
	out := make(map[int32]float64, len(procs))
	for _, p := range procs {
		if t, err := p.Times(); err == nil {
			out[p.Pid] = t.User + t.System
		}
	}
	return out
}

// measure computes per-process CPU from two readings and sorts the result.
func measure(procs []*process.Process, before map[int32]float64, interval time.Duration) []Process {
	after := cpuTimes(procs)
	var result []Process
	for _, p := range procs {
		b, ok1 := before[p.Pid]
		a, ok2 := after[p.Pid]
		if !ok1 || !ok2 {
			continue // process was present in only one reading
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

			Origin: readOrigin(p.Pid),
		})
	}
	sortByCPU(result)
	return result
}

// sortByCPU sorts descending by CPU, ties by PID (stable output).
func sortByCPU(ps []Process) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].CPU != ps[j].CPU {
			return ps[i].CPU > ps[j].CPU
		}
		return ps[i].PID < ps[j].PID
	})
}

// Top returns the first n processes by CPU.
func Top(ps []Process, n int) []Process {
	sorted := append([]Process(nil), ps...) // copy so the input stays untouched
	sortByCPU(sorted)
	if n < len(sorted) {
		sorted = sorted[:n]
	}
	return sorted
}

// skipMount reports whether a mount is noise for disk monitoring:
// snap images and the EFI boot partition.
func skipMount(path, fstype string) bool {
	return fstype == "squashfs" || path == "/boot/efi" || strings.HasPrefix(path, "/boot/efi/")
}

// mounts returns real filesystems without duplicates (bind mounts).
func mounts() []Mount {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Mount
	for _, p := range parts {
		if seen[p.Device] || skipMount(p.Mountpoint, p.Fstype) {
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
