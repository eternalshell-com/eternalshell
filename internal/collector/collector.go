// Package collector reads system values (CPU, memory, load, disks, processes).
// It only reads; it never changes anything on the server.
package collector

import (
	"context"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
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

	// Start is the process start time in clock ticks since boot. Together with
	// the PID it identifies one process: a reused PID has a different Start.
	Start   uint64
	Cmdline string // full command line, capped; empty for kernel threads
	User    string // owner of the process

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
	MountSkips []MountSkip // real mounts that could not be read
	Processes  []Process   // all readable processes, sorted by CPU descending

	Containers []Container // empty if Docker is unreachable
	DockerErr  error       // why Docker data is missing; not a failure of the snapshot

	// Disk is the latest Docker disk usage (refreshed in the background every
	// 5 minutes). It is nil until the first refresh has finished, and stays nil
	// if disk usage was not enabled. DiskErr is the error of the latest refresh.
	Disk    *DiskUsage
	DiskErr error
}

// MountSkip is a mounted filesystem that was left out because it cannot be read.
type MountSkip struct {
	Path   string
	Reason string
}

// procMeta is what we remember about a process so it is read only once:
// things that do not change while the process lives.
type procMeta struct {
	start   uint64 // identifies the process; a reused PID has a different start
	name    string
	cmdline string
	user    string
	kernel  bool
	origin  Origin
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

	ctx    context.Context // cancelled by Close; stops a running disk refresh
	cancel context.CancelFunc
	disk   diskState
	sizes  *sizeCache // folder sizes, only touched by the (single) disk refresh
}

// diskState is the background disk usage refresh. It is the one part of the
// Collector that is touched by more than one goroutine, so it has a mutex.
type diskState struct {
	mu        sync.Mutex
	enabled   bool
	running   bool
	lastStart time.Time
	result    *DiskUsage
	err       error
}

// New creates a Collector. It does not connect to Docker yet.
func New() *Collector {
	ctx, cancel := context.WithCancel(context.Background())
	return &Collector{
		docker:   newDocker(),
		meta:     map[int32]procMeta{},
		pageSize: uint64(os.Getpagesize()),
		ctx:      ctx,
		cancel:   cancel,
		sizes:    newSizeCache(folderEvery),
	}
}

// Close stops a running disk refresh and releases the Docker client.
func (c *Collector) Close() {
	c.cancel()
	c.docker.close()
}

// EnableDiskUsage turns on the background Docker disk usage. It is off by
// default so that quick commands like `status` never wait for it.
func (c *Collector) EnableDiskUsage() {
	c.disk.mu.Lock()
	c.disk.enabled = true
	c.disk.mu.Unlock()
}

// refreshDisk starts a background refresh if one is due, and returns the latest
// result. It never waits for the refresh: the Docker call can take seconds, and
// with big bind-mount directories much longer.
func (c *Collector) refreshDisk(now time.Time) (*DiskUsage, error) {
	d := &c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return nil, nil
	}
	if !d.running && (d.lastStart.IsZero() || now.Sub(d.lastStart) >= diskEvery) {
		d.running = true
		d.lastStart = now
		go c.runDiskRefresh()
	}
	return d.result, d.err
}

// runDiskRefresh is the body of the background goroutine.
func (c *Collector) runDiskRefresh() {
	ctx, cancel := context.WithTimeout(c.ctx, diskTimeout)
	defer cancel()
	usage, err := c.docker.diskUsage(ctx, c.sizes)

	d := &c.disk
	d.mu.Lock()
	defer d.mu.Unlock()
	d.running = false
	if err != nil {
		d.err = err
		d.lastStart = time.Now().Add(diskRetryAfter - diskEvery) // try again sooner
		return
	}
	d.result, d.err = usage, nil
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
	s.Mounts, s.MountSkips = mounts()
	s.Disk, s.DiskErr = c.refreshDisk(time.Now())
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

			Start:   st.start,
			Cmdline: m.cmdline,
			User:    m.user,
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
		start:   st.start,
		name:    processName(st.comm, cmdline),
		cmdline: joinCmdline(cmdline),
		user:    readUser(pid),
		kernel:  isKernelThread(pid, st.ppid, cmdline),
		origin:  readOrigin(pid),
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

// mounts returns real filesystems without duplicates (bind mounts). A mount
// that cannot be read, for example because of the service's hardening, is not
// an error: it is returned in the second list so the caller can say so once.
func mounts() ([]Mount, []MountSkip) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []Mount
	var skipped []MountSkip
	for _, p := range parts {
		if seen[p.Device] || skipMount(p.Mountpoint, p.Fstype) {
			continue
		}
		seen[p.Device] = true
		u, err := disk.Usage(p.Mountpoint)
		switch {
		case err != nil:
			skipped = append(skipped, MountSkip{Path: p.Mountpoint, Reason: err.Error()})
		case u.Total == 0:
			skipped = append(skipped, MountSkip{Path: p.Mountpoint, Reason: "reports a size of 0"})
		default:
			out = append(out, Mount{Path: p.Mountpoint, Total: u.Total, Used: u.Used, UsedPercent: u.UsedPercent})
		}
	}
	return out, skipped
}
