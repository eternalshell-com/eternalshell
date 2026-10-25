package watch

import (
	"fmt"
	"sort"
	"strings"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
	"eternalshell/internal/mail"
)

// topProjects is how many projects the disk mail lists.
const topProjects = 5

func diskAlertMessage(n Notice, d Details) mail.Message {
	mount := n.Key.Mount
	free := ""
	if d.Mount != nil {
		free = fmt.Sprintf(" (%s free)", format.Bytes(d.Mount.Total-d.Mount.Used))
	}
	subject := fmt.Sprintf("[EternalShell] %s - disk %s at %s%s", d.Server, mount, format.Percent(n.Avg), free)

	var b strings.Builder
	fmt.Fprintf(&b, "The filesystem mounted at %s is %s full. Threshold: %s.\n", mount, format.Percent(n.Avg), format.Percent(n.Threshold))

	b.WriteString("\nMount\n")
	fmt.Fprintf(&b, "  Path:  %s\n", mount)
	if m := d.Mount; m != nil {
		fmt.Fprintf(&b, "  Used:  %s of %s (%s)\n", format.Bytes(m.Used), format.Bytes(m.Total), format.Percent(m.UsedPercent))
		fmt.Fprintf(&b, "  Free:  %s\n", format.Bytes(m.Total-m.Used))
	}

	writeDiskConsumers(&b, d, mount)
	writeServer(&b, d)
	b.WriteString("\n--\nYou will get one all-clear mail when this is over. No further mails about this incident until then.\n")
	return mail.Message{Subject: subject, Body: b.String()}
}

func diskClearMessage(n Notice, d Details) mail.Message {
	mount := n.Key.Mount
	var b strings.Builder
	var subject string
	if n.Reason == ReasonGone {
		subject = fmt.Sprintf("[EternalShell] %s - all clear: disk %s is no longer mounted", d.Server, mount)
		fmt.Fprintf(&b, "The filesystem at %s is no longer mounted, so its disk alert is closed.\n", mount)
	} else {
		subject = fmt.Sprintf("[EternalShell] %s - all clear: disk %s back to %s", d.Server, mount, format.Percent(n.Avg))
		fmt.Fprintf(&b, "The filesystem at %s is back to %s used (alert threshold: %s).\n", mount, format.Percent(n.Avg), format.Percent(n.Threshold))
	}
	if n.Lasted > 0 {
		fmt.Fprintf(&b, "The incident lasted about %s after the alert.\n", duration(n.Lasted))
	}
	if m := d.Mount; m != nil {
		fmt.Fprintf(&b, "\n  Used:  %s of %s\n  Free:  %s\n", format.Bytes(m.Used), format.Bytes(m.Total), format.Bytes(m.Total-m.Used))
	}
	writeServer(&b, d)
	return mail.Message{Subject: subject, Body: b.String()}
}

// consumersState says what the disk mails can tell about Docker's use of a mount.
type consumersState int

const (
	csNone        consumersState = iota // nothing to say about this mount
	csUnavailable                       // Docker disk usage failed (Err)
	csNotMeasured                       // the first measurement is not there yet
	csShown                             // there are figures
)

// consumers is what the disk mails say about Docker's use of one mount. The text
// and the HTML mail both read it, so their numbers cannot differ.
type consumers struct {
	State consumersState
	Err   error

	OnDockerMount bool                    // the mount holds Docker's own data
	Projects      []collector.ProjectDisk // the biggest ones, only on Docker's mount
	Dirs          []diskDir               // project folders and bind directories on this mount, biggest first
	CrossMount    bool                    // some folders or binds are on other mounts

	Usage *collector.DiskUsage

	OtherKnown bool  // the mount itself is known, so "other" can be computed
	OtherBytes int64 // used on the mount minus what Docker accounts for there
	Approx     bool  // some figures could not be measured completely
}

// consumersFor works out what Docker uses on a mount. It has something to say
// for the mount that holds Docker's data and for any mount that holds
// bind-mounted project directories. Other mounts get nothing.
func consumersFor(d Details, mount string) consumers {
	snap := d.Snap
	if snap == nil {
		return consumers{State: csNone}
	}
	u := snap.Disk
	if u == nil {
		if snap.DiskErr != nil {
			return consumers{State: csUnavailable, Err: snap.DiskErr}
		}
		return consumers{State: csNotMeasured}
	}

	c := consumers{Usage: u, Approx: u.Unknown}
	if root := collector.MountFor(snap.Mounts, u.RootDir); root != nil && u.RootDir != "" {
		c.OnDockerMount = root.Path == mount
	}

	// Project folders and bind directories that live on this mount.
	var here []diskDir
	var hereBytes int64
	for _, f := range u.Folders {
		if m := collector.MountFor(snap.Mounts, f.Path); m != nil && m.Path == mount {
			here = append(here, diskDir{Path: f.Path, Bytes: f.Bytes, Known: f.Known, Label: "project " + strings.Join(f.Projects, ", ")})
			hereBytes += f.Bytes
		}
	}
	for _, bs := range u.Binds {
		if m := collector.MountFor(snap.Mounts, bs.Path); m != nil && m.Path == mount {
			here = append(here, diskDir{Path: bs.Path, Bytes: bs.Bytes, Known: bs.Known, Label: "bind mount"})
			hereBytes += bs.Bytes
		}
	}
	if !c.OnDockerMount && len(here) == 0 {
		return consumers{State: csNone}
	}
	c.State = csShown
	c.CrossMount = len(here) < len(u.Folders)+len(u.Binds)
	c.Dirs = sortedDirs(here)

	// What Docker accounts for on this mount, to subtract from "used".
	accounted := hereBytes
	if c.OnDockerMount {
		accounted += u.Layers + u.Logs + u.Volumes + u.Images + u.BuildCache
		for _, p := range u.Projects {
			if len(c.Projects) == topProjects || p.Total() == 0 {
				break
			}
			c.Projects = append(c.Projects, p)
		}
	}
	if d.Mount != nil {
		c.OtherKnown = true
		c.OtherBytes = max(int64(d.Mount.Used)-accounted, 0)
	}
	return c
}

// otherText is "Other (not Docker)": "-" if unknown, "~" in front if some
// figures are approximate.
func (c consumers) otherText() string {
	if !c.OtherKnown {
		return "-"
	}
	prefix := ""
	if c.Approx {
		prefix = "~"
	}
	return prefix + format.Bytes(uint64(c.OtherBytes))
}

// projectName is how a project is named in the list: containers say so.
func projectName(p collector.ProjectDisk) string {
	if p.Standalone {
		return p.Name + " (container)"
	}
	return p.Name
}

// writeDiskConsumers lists what Docker uses on a mount.
func writeDiskConsumers(b *strings.Builder, d Details, mount string) {
	c := consumersFor(d, mount)
	switch c.State {
	case csNone:
		return
	case csUnavailable:
		fmt.Fprintf(b, "\nDocker disk usage is not available: %v\n", c.Err)
		return
	case csNotMeasured:
		b.WriteString("\nDocker disk usage has not been measured yet.\n")
		return
	}
	u := c.Usage

	b.WriteString("\nBiggest consumers (Docker)\n")
	if c.OnDockerMount {
		for _, p := range c.Projects {
			fmt.Fprintf(b, "  %-26.26s %9s  (layers %s, volumes %s, files %s, binds %s, logs %s)\n", projectName(p),
				format.BytesPartial(p.Total(), !p.Complete()),
				format.Bytes(uint64(p.Layers)),
				format.BytesPartial(p.Volumes, p.VolumesUnknown),
				format.BytesPartial(p.Files, p.FilesUnknown),
				format.BytesPartial(p.Binds, p.BindsUnknown),
				format.BytesPartial(p.Logs, p.LogsUnknown))
		}
		if len(c.Projects) == 0 {
			b.WriteString("  (no containers)\n")
		}
		fmt.Fprintf(b, "  %-26s %9s\n", "Images", format.Bytes(uint64(u.Images)))
		fmt.Fprintf(b, "  %-26s %9s\n", "Build cache", format.Bytes(uint64(u.BuildCache)))
		fmt.Fprintf(b, "  %-26s %9s  (unused volumes %s + dangling images %s)\n", "Reclaimable",
			format.Bytes(uint64(u.Reclaimable)), format.Bytes(uint64(u.UnusedVolumes)), format.Bytes(uint64(u.DanglingImg)))
		if c.CrossMount {
			b.WriteString("  Note: project figures include folders and bind mounts on other mounts; those are not subtracted below.\n")
		}
	} else {
		b.WriteString("  Project folders and bind mounts on this mount:\n")
		for i, dir := range c.Dirs {
			if i == topProjects {
				break
			}
			fmt.Fprintf(b, "  %-40.40s %9s  (%s)\n", dir.Path, format.BytesPartial(dir.Bytes, !dir.Known), dir.Label)
		}
	}

	fmt.Fprintf(b, "  %-26s %9s\n", "Other (not Docker)", c.otherText())
	if c.Approx {
		b.WriteString("  \"?\" or \"+?\": part of that could not be read or measured; \"~\" means the figure is approximate.\n")
	}
}

// diskDir is a project folder or bind directory on a mount, for the mail.
type diskDir struct {
	Path  string
	Bytes int64
	Known bool
	Label string
}

// sortedDirs returns the directories biggest first, without changing the input.
func sortedDirs(in []diskDir) []diskDir {
	out := append([]diskDir(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}
