package collector

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// This file reads /proc directly for the values that are needed for every
// process on every tick. One read of /proc/<pid>/stat gives CPU time, start
// time, parent PID and resident memory at once, where the gopsutil process
// API needs a separate file read for each of them.

// clockTicks is how many CPU-time ticks /proc reports per second (USER_HZ).
// It is 100 on every mainstream Linux kernel build.
const clockTicks = 100

var errBadStat = errors.New("malformed stat line")

// procStat is the part of /proc/<pid>/stat we use.
type procStat struct {
	comm       string  // executable name, at most 15 characters
	ppid       int32   // parent PID
	cpuSeconds float64 // user + system CPU time used so far
	start      uint64  // start time in clock ticks since boot; identifies the process
	rssPages   uint64  // resident memory in pages
}

// parseProcStat parses one /proc/<pid>/stat line:
//
//	1234 (name with spaces) S 1 1234 ... utime stime ... starttime ... rss ...
//
// The name sits in parentheses and may itself contain spaces and parentheses,
// so the fields are counted from the *last* closing parenthesis.
func parseProcStat(b []byte) (procStat, error) {
	s := string(b)
	open := strings.IndexByte(s, '(')
	end := strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return procStat{}, errBadStat
	}
	f := strings.Fields(s[end+1:])
	// f[0] is field 3 (state), so field N is f[N-3]. We need up to field 24 (rss).
	if len(f) < 22 {
		return procStat{}, errBadStat
	}

	var st procStat
	st.comm = s[open+1 : end]
	ppid, err1 := strconv.ParseInt(f[1], 10, 32)
	utime, err2 := strconv.ParseUint(f[11], 10, 64)
	stime, err3 := strconv.ParseUint(f[12], 10, 64)
	start, err4 := strconv.ParseUint(f[19], 10, 64)
	rss, err5 := strconv.ParseInt(f[21], 10, 64)
	if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
		return procStat{}, errBadStat
	}
	st.ppid = int32(ppid)
	st.cpuSeconds = float64(utime+stime) / clockTicks
	st.start = start
	st.rssPages = uint64(max(rss, 0))
	return st, nil
}

func readProcStat(pid int32) (procStat, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, err
	}
	return parseProcStat(b)
}

// readCmdline returns the command line of a process; empty for kernel threads
// and zombies.
func readCmdline(pid int32) []string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// listPIDs returns the PIDs of all processes currently in /proc.
func listPIDs() ([]int32, error) {
	d, err := os.Open("/proc")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1) // names only; no per-entry stat call
	if err != nil {
		return nil, err
	}
	pids := make([]int32, 0, len(names))
	for _, n := range names {
		if pid, err := strconv.ParseInt(n, 10, 32); err == nil {
			pids = append(pids, int32(pid))
		}
	}
	return pids, nil
}

// parseCPULine parses the first line of /proc/stat:
//
//	cpu  user nice system idle iowait irq softirq steal guest guest_nice
//
// busy and total are in clock ticks. Guest time is already part of user time,
// so only the first eight values are summed.
func parseCPULine(line string) (busy, total float64, err error) {
	f := strings.Fields(line)
	if len(f) < 9 || f[0] != "cpu" {
		return 0, 0, errBadStat
	}
	var idle float64
	for i, v := range f[1:9] {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, 0, errBadStat
		}
		total += n
		if i == 3 || i == 4 { // idle, iowait
			idle += n
		}
	}
	return total - idle, total, nil
}

// readCPUTimes reads the machine-wide CPU counters from /proc/stat.
func readCPUTimes() (busy, total float64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil {
		return 0, 0, err
	}
	return parseCPULine(line)
}
