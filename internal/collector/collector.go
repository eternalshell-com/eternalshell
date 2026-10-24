// Package collector reads system values (CPU, memory, load, disks, processes).
// It only reads; it never changes anything on the server.
package collector

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Process describes a process at measurement time.
// CPU is per core like top: 100 means one core fully used.
type Process struct {
	PID  int32
	Name string
	RSS  uint64 // resident memory in bytes
	PPID int32

	// CPU is only meaningful if CPUKnown is set. It is unknown for the first
	// snapshot and for processes that appeared since the previous one.
	CPU      float64
	CPUKnown bool

	// Kernel marks kernel threads (kthreadd and its children). They have no
	// command line. Zombies have none either and are counted here too, which is
	// fine: they use no CPU or memory worth showing.
	Kernel bool

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
	CPUPercent float64 // whole server, 0-100; only valid if CPUKnown
	CPUKnown   bool    // false for the first snapshot
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

// procMeta is what we remember about a process so it is read only once:
// things that do not change while the process lives.
type procMeta struct {
	start  uint64 // identifies the process; a reused PID has a different start
	name   string
	kernel bool
	origin Origin
}

// procTimes is the part of the previous sample needed to compute CPU deltas.
type procTimes struct {
	start uint64
	cpu   float64 // CPU seconds used so far
}

// sample is one reading of the CPU counters, kept until the next snapshot.
type sample struct {
	at          time.Time
	busy, total float64 // machine-wide, in clock ticks
	procs       map[int32]procTimes
}

// Collector takes snapshots. It keeps state between snapshots (the previous
// CPU sample, per-process metadata, the Docker client and its restart-count
// cache), so create it once and reuse it. CPU values are the change since the
// previous call to Collect, so the first snapshot has none.
//
// A Collector is not safe for concurrent use: call Collect from one goroutine
// at a time.
type Collector struct {
	docker   *Docker
	prev     *sample
	meta     map[int32]procMeta
	pageSize uint64
}

// New creates a Collector. It does not connect to Docker yet.
func New() *Collector {
	return &Collector{
		docker:   newDocker(),
		meta:     map[int32]procMeta{},
		pageSize: uint64(os.Getpagesize()),
	}
}

// Close releases the Docker client.
func (c *Collector) Close() {
	c.docker.close()
}

type dockerResult struct {
	containers []Container
	err        error
}

// Collect reads the current state. CPU values are averages over the time since
// the previous call; the first call has none (CPUKnown is false).
// If Docker is unreachable the snapshot is still returned, with DockerErr set.
func (c *Collector) Collect() (*Snapshot, error) {
	s := &Snapshot{}

	// Ask Docker in a goroutine while we read /proc.
	// A buffered channel lets the goroutine finish even if nobody receives.
	dockerCh := make(chan dockerResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		defer cancel()
		cs, err := c.docker.list(ctx)
		dockerCh <- dockerResult{cs, err}
	}()

	busy, total, err := readCPUTimes()
	if err != nil {
		return nil, err
	}
	cur := &sample{at: time.Now(), busy: busy, total: total, procs: map[int32]procTimes{}}

	if c.prev != nil && total > c.prev.total {
		s.CPUPercent = (busy - c.prev.busy) / (total - c.prev.total) * 100
		s.CPUKnown = true
	}

	procs, err := c.sweep(cur)
	if err != nil {
		return nil, err
	}
	s.Processes = procs
	c.prev = cur

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

// sweep reads every process once. It records the CPU times in cur and compares
// them with the previous sample to get CPU percentages.
func (c *Collector) sweep(cur *sample) ([]Process, error) {
	pids, err := listPIDs()
	if err != nil {
		return nil, err
	}
	var elapsed float64
	if c.prev != nil {
		elapsed = cur.at.Sub(c.prev.at).Seconds()
	}

	result := make([]Process, 0, len(pids))
	for _, pid := range pids {
		st, err := readProcStat(pid)
		if err != nil {
			continue // exited meanwhile, or not readable
		}
		cur.procs[pid] = procTimes{start: st.start, cpu: st.cpuSeconds}

		m, ok := c.meta[pid]
		if !ok || m.start != st.start {
			m = newMeta(pid, st)
			c.meta[pid] = m
		}

		p := Process{
			PID:    pid,
			Name:   m.name,
			PPID:   st.ppid,
			RSS:    st.rssPages * c.pageSize,
			Kernel: m.kernel,
			Origin: m.origin,
		}
		if c.prev != nil {
			if before, ok := c.prev.procs[pid]; ok {
				p.CPU, p.CPUKnown = cpuPercent(before, cur.procs[pid], elapsed)
			}
		}
		result = append(result, p)
	}

	for pid := range c.meta { // forget processes that are gone
		if _, ok := cur.procs[pid]; !ok {
			delete(c.meta, pid)
		}
	}
	sortByCPU(result)
	return result, nil
}

// newMeta reads the things that do not change during a process's life.
func newMeta(pid int32, st procStat) procMeta {
	cmdline := readCmdline(pid)
	return procMeta{
		start:  st.start,
		name:   processName(st.comm, cmdline),
		kernel: isKernelThread(pid, st.ppid, cmdline),
		origin: readOrigin(pid),
	}
}

// processName returns the display name. The kernel cuts comm at 15 characters;
// if the command line starts with a longer name that matches, use that one.
func processName(comm string, cmdline []string) string {
	if len(comm) >= 15 && len(cmdline) > 0 {
		if base := path.Base(cmdline[0]); strings.HasPrefix(base, comm) {
			return base
		}
	}
	return comm
}

// cpuPercent computes CPU use between two samples of the same process, per core
// (100 = one core). ok is false if the two samples are not comparable: the PID
// was reused by another process, or no time has passed.
func cpuPercent(before, now procTimes, elapsed float64) (percent float64, ok bool) {
	if before.start != now.start || elapsed <= 0 {
		return 0, false
	}
	return max(now.cpu-before.cpu, 0) / elapsed * 100, true
}

// isKernelThread reports whether a process is a kernel thread: kthreadd itself
// (PID 2), its children (PPID 2), or anything without a command line.
func isKernelThread(pid, ppid int32, cmdline []string) bool {
	return pid == 2 || ppid == 2 || len(cmdline) == 0
}

// UserProcesses returns ps without kernel threads, in the same order.
func UserProcesses(ps []Process) []Process {
	out := make([]Process, 0, len(ps))
	for _, p := range ps {
		if !p.Kernel {
			out = append(out, p)
		}
	}
	return out
}

// sortByCPU sorts descending by CPU, processes with unknown CPU last, ties by
// PID (stable output).
func sortByCPU(ps []Process) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].CPUKnown != ps[j].CPUKnown {
			return ps[i].CPUKnown
		}
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
