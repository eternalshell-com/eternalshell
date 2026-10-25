package watch

import (
	"fmt"
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

// writeDiskConsumers lists what Docker uses on a mount. It shows something for
// the mount that holds Docker's data, and for any mount that holds bind-mounted
// project directories. Other mounts get nothing.
func writeDiskConsumers(b *strings.Builder, d Details, mount string) {
	snap := d.Snap
	if snap == nil {
		return
	}
	u := snap.Disk
	if u == nil {
		switch {
		case snap.DiskErr != nil:
			fmt.Fprintf(b, "\nDocker disk usage is not available: %v\n", snap.DiskErr)
		default:
			b.WriteString("\nDocker disk usage has not been measured yet.\n")
		}
		return
	}

	onDockerMount := false
	if root := collector.MountFor(snap.Mounts, u.RootDir); root != nil && u.RootDir != "" {
		onDockerMount = root.Path == mount
	}
	var bindsHere []collector.BindShare
	var bindsHereBytes int64
	for _, bs := range u.Binds {
		if m := collector.MountFor(snap.Mounts, bs.Path); m != nil && m.Path == mount {
			bindsHere = append(bindsHere, bs)
			bindsHereBytes += bs.Bytes
		}
	}
	if !onDockerMount && len(bindsHere) == 0 {
		return
	}

	// What Docker accounts for on this mount, to subtract from "used".
	accounted := bindsHereBytes
	if onDockerMount {
		accounted += u.Layers + u.Logs + u.Volumes + u.Images + u.BuildCache
	}

	b.WriteString("\nBiggest consumers (Docker)\n")
	if onDockerMount {
		shown := 0
		for _, p := range u.Projects {
			if shown == topProjects || p.Total() == 0 {
				break
			}
			shown++
			name := p.Name
			if p.Standalone {
				name += " (container)"
			}
			fmt.Fprintf(b, "  %-26.26s %9s  (layers %s, volumes %s, binds %s, logs %s)\n", name,
				format.BytesPartial(p.Total(), !p.Complete()),
				format.Bytes(uint64(p.Layers)),
				format.BytesPartial(p.Volumes, p.VolumesUnknown),
				format.BytesPartial(p.Binds, p.BindsUnknown),
				format.BytesPartial(p.Logs, p.LogsUnknown))
		}
		if shown == 0 {
			b.WriteString("  (no containers)\n")
		}
		fmt.Fprintf(b, "  %-26s %9s\n", "Images", format.Bytes(uint64(u.Images)))
		fmt.Fprintf(b, "  %-26s %9s\n", "Build cache", format.Bytes(uint64(u.BuildCache)))
		fmt.Fprintf(b, "  %-26s %9s  (unused volumes %s + dangling images %s)\n", "Reclaimable",
			format.Bytes(uint64(u.Reclaimable)), format.Bytes(uint64(u.UnusedVolumes)), format.Bytes(uint64(u.DanglingImg)))
		if len(bindsHere) < len(u.Binds) {
			b.WriteString("  Note: project figures include bind-mounted directories on other mounts; those are not subtracted below.\n")
		}
	} else {
		b.WriteString("  Bind-mounted project directories on this mount:\n")
		shown := 0
		for _, bs := range sortedBinds(bindsHere) {
			if shown == topProjects {
				break
			}
			shown++
			fmt.Fprintf(b, "  %-40.40s %9s\n", bs.Path, format.BytesPartial(bs.Bytes, !bs.Known))
		}
	}

	other := "-"
	if d.Mount != nil {
		o := int64(d.Mount.Used) - accounted
		prefix := ""
		if u.Unknown {
			prefix = "~"
		}
		if o < 0 {
			o = 0
		}
		other = prefix + format.Bytes(uint64(o))
	}
	fmt.Fprintf(b, "  %-26s %9s\n", "Other (not Docker)", other)
	if u.Unknown {
		b.WriteString("  \"?\" or \"+?\": part of that could not be read or measured; \"~\" means the figure is approximate.\n")
	}
}

// sortedBinds returns the bind directories biggest first, without changing the input.
func sortedBinds(in []collector.BindShare) []collector.BindShare {
	out := append([]collector.BindShare(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Bytes > out[j-1].Bytes; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
