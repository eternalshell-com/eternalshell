package report

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"eternalshell/internal/collector"
	"eternalshell/internal/config"
	"eternalshell/internal/domains"
)

// Input is what Build needs. Journal is nil for the command line, which has no
// running watcher to ask.
type Input struct {
	Snap    *collector.Snapshot
	Config  *config.Config
	Prev    *State
	Journal *Journal
	Now     time.Time
}

// domainCheckCap bounds the checking of all domains for one report.
const domainCheckCap = 60 * time.Second

// Build gathers everything the report needs: the snapshot, the state of the
// domains (one GET per host), the uptime, and, when the watcher gives its
// journal, the CPU history and the alerts of the period.
func Build(ctx context.Context, in Input) Data {
	name := in.Config.Server.Name
	if name == "" {
		name, _ = os.Hostname()
	}
	d := Data{
		Name:             name,
		Now:              in.Now,
		Prev:             in.Prev,
		DiskAlertPercent: in.Config.Alerts.Disk.Percent,
		Mounts:           in.Snap.Mounts,
		Containers:       in.Snap.Containers,
		Disk:             in.Snap.Disk,
		DiskErr:          in.Snap.DiskErr,
	}

	if up, err := readUptime(); err == nil {
		d.Uptime, d.BootTime = up, in.Now.Add(-up)
	}

	// Domains: the hosts of all containers, checked together.
	owners := collector.HostOwners(in.Snap.Containers)
	if len(owners) > 0 {
		hosts := make([]string, len(owners))
		for i, o := range owners {
			hosts[i] = o.Host
		}
		cctx, cancel := context.WithTimeout(ctx, domainCheckCap)
		results := domains.CheckAll(cctx, hosts)
		cancel()
		for _, o := range owners {
			d.Domains = append(d.Domains, DomainResult{Host: o.Host, Project: o.Project, Result: results[o.Host]})
		}
	}

	if in.Journal != nil {
		d.Watcher, d.WatcherSince = true, in.Journal.Started
		d.CPU = in.Journal.CPU()
		d.Incidents = in.Journal.IncidentsSince(d.watchSince())
	}

	cur := SnapshotState(d)
	d.Diff = Diff(in.Prev, &cur)
	return d
}

// SnapshotState is the state to save after a report, so the next one can show
// what changed.
func SnapshotState(d Data) State {
	s := State{
		Version:    stateVersion,
		Time:       d.Now,
		BootTime:   d.BootTime,
		Mounts:     map[string]MountState{},
		Containers: map[string][]string{},
	}
	for _, m := range d.Mounts {
		s.Mounts[m.Path] = MountState{Used: m.Used, Total: m.Total}
	}
	if d.Disk != nil {
		s.DiskKnown = true
		s.Projects = map[string]int64{}
		for _, p := range d.Disk.Projects {
			key := projectKey(p.Name)
			if p.Standalone {
				key = containerKey(p.Name)
			}
			s.Projects[key] = p.Total()
		}
		s.Reclaimable = d.Disk.Reclaimable
	}
	for _, c := range d.Containers {
		group, name := standaloneGroup, c.Name
		if c.Project != "" {
			group = c.Project
			if c.Service != "" {
				name = c.Service
			}
		}
		s.Containers[group] = append(s.Containers[group], name)
	}
	return s
}

// CollectSnapshot takes a snapshot for a report outside the watcher. The Docker
// disk usage is measured in the background and can take a while, so it waits for
// it (until ctx ends, then it goes on without) and says so on progress.
func CollectSnapshot(ctx context.Context, col *collector.Collector, progress io.Writer) (*collector.Snapshot, error) {
	col.EnableDiskUsage()
	told := false
	for {
		snap, err := col.Collect()
		if err != nil {
			return nil, err
		}
		if snap.Disk != nil || snap.DiskErr != nil {
			return snap, nil
		}
		if progress != nil && !told {
			told = true
			fmt.Fprintln(progress, "Measuring the Docker disk usage (this can take a while)...")
		}
		select {
		case <-ctx.Done():
			return snap, nil
		case <-time.After(2 * time.Second):
		}
	}
}

// readUptime reads how long the machine has been running from /proc/uptime.
func readUptime() (time.Duration, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	return parseUptime(string(b))
}

func parseUptime(s string) (time.Duration, error) {
	field, _, _ := strings.Cut(strings.TrimSpace(s), " ")
	secs, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot read the uptime from %q", s)
	}
	return time.Duration(secs * float64(time.Second)), nil
}
