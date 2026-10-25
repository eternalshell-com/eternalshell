package watch

import (
	"fmt"
	"strings"
	"time"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
	"eternalshell/internal/mail"
)

// Details is everything a mail text needs besides the notice itself.
type Details struct {
	Server string
	Now    time.Time

	// Proc is the process of a cpu-process notice: the current one, or the last
	// one seen if it has exited. Nil if it was never seen.
	Proc *collector.Process

	// Snap is the current state of the server (total CPU, load, top processes,
	// Docker disk usage).
	Snap *collector.Snapshot

	// Mount is the filesystem of a disk notice; nil if it is gone.
	Mount *collector.Mount
}

// Message builds the mail for a notice: the plain text, and an HTML version of
// the same facts.
func Message(n Notice, d Details) mail.Message {
	m := textMessage(n, d)
	m.HTML = htmlMessage(n, d)
	return m
}

// textMessage builds the plain text mail for a notice.
func textMessage(n Notice, d Details) mail.Message {
	if n.Key.Rule == RuleDisk {
		if n.Kind == KindClear {
			return diskClearMessage(n, d)
		}
		return diskAlertMessage(n, d)
	}
	if n.Kind == KindClear {
		return clearMessage(n, d)
	}
	return alertMessage(n, d)
}

func alertMessage(n Notice, d Details) mail.Message {
	var b strings.Builder

	var subject string
	if n.Key.Rule == RuleProcess {
		name, pid := procLabel(n, d)
		subject = fmt.Sprintf("[EternalShell] %s - %s (PID %s) at %s CPU for %s",
			d.Server, name, pid, format.Percent(n.Avg), duration(n.Window))
		fmt.Fprintf(&b, "%s (PID %s) has used %s of one core on average over the last %s.\n",
			name, pid, format.Percent(n.Avg), duration(n.Window))
		fmt.Fprintf(&b, "Threshold: %s of one core for %s.\n", format.Percent(n.Threshold), duration(n.Window))
		writeProcess(&b, d.Proc)
	} else {
		subject = fmt.Sprintf("[EternalShell] %s - server CPU at %s for %s",
			d.Server, format.Percent(n.Avg), duration(n.Window))
		fmt.Fprintf(&b, "The whole server has used %s CPU on average over the last %s.\n",
			format.Percent(n.Avg), duration(n.Window))
		fmt.Fprintf(&b, "Threshold: %s for %s.\n", format.Percent(n.Threshold), duration(n.Window))
	}

	writeServer(&b, d)
	if n.Key.Rule == RuleTotal {
		b.WriteString("\nLikely cause: the processes using the most CPU right now:\n")
	} else {
		b.WriteString("\nProcesses using the most CPU right now:\n")
	}
	writeTop(&b, d.Snap)

	b.WriteString("\n--\nYou will get one all-clear mail when this is over. No further mails about this incident until then.\n")
	return mail.Message{Subject: subject, Body: b.String()}
}

func clearMessage(n Notice, d Details) mail.Message {
	var b strings.Builder
	var subject string

	if n.Key.Rule == RuleProcess {
		name, pid := procLabel(n, d)
		switch n.Reason {
		case ReasonExited:
			subject = fmt.Sprintf("[EternalShell] %s - all clear: %s (PID %s) has exited", d.Server, name, pid)
			fmt.Fprintf(&b, "%s (PID %s) has exited. The high CPU use is over.\n", name, pid)
		default:
			subject = fmt.Sprintf("[EternalShell] %s - all clear: %s (PID %s) is calm again", d.Server, name, pid)
			fmt.Fprintf(&b, "%s (PID %s) is back below %s of one core (average of the last minute: %s).\n",
				name, pid, format.Percent(n.Threshold), format.Percent(n.Avg))
		}
	} else {
		subject = fmt.Sprintf("[EternalShell] %s - all clear: server CPU back to %s", d.Server, format.Percent(n.Avg))
		fmt.Fprintf(&b, "Server CPU is back below %s (average of the last minute: %s).\n",
			format.Percent(n.Threshold), format.Percent(n.Avg))
	}
	if n.Lasted > 0 {
		fmt.Fprintf(&b, "The incident lasted about %s after the alert.\n", duration(n.Lasted))
	}
	if n.Key.Rule == RuleProcess {
		writeProcess(&b, d.Proc)
	}

	writeServer(&b, d)
	return mail.Message{Subject: subject, Body: b.String()}
}

// procLabel returns the name and PID for texts; "unknown" if the process was never seen.
func procLabel(n Notice, d Details) (name, pid string) {
	if d.Proc != nil {
		return d.Proc.Name, fmt.Sprint(d.Proc.PID)
	}
	return "unknown process", fmt.Sprint(n.Key.PID)
}

func writeProcess(b *strings.Builder, p *collector.Process) {
	if p == nil {
		return
	}
	b.WriteString("\nProcess\n")
	fmt.Fprintf(b, "  Name:     %s\n", p.Name)
	fmt.Fprintf(b, "  PID:      %d\n", p.PID)
	fmt.Fprintf(b, "  Command:  %s\n", orDash(p.Cmdline))
	fmt.Fprintf(b, "  User:     %s\n", orDash(p.User))
	fmt.Fprintf(b, "  Origin:   %s\n", DescribeOrigin(p.Origin))
	if p.Origin.Kind == collector.KindContainer && p.Origin.ContainerName != "" {
		fmt.Fprintf(b, "  Check:    docker logs %s\n", p.Origin.ContainerName)
	}
}

func writeServer(b *strings.Builder, d Details) {
	b.WriteString("\nServer\n")
	fmt.Fprintf(b, "  Name:       %s\n", d.Server)
	fmt.Fprintf(b, "  Time:       %s\n", d.Now.UTC().Format("2006-01-02 15:04:05 UTC"))
	if s := d.Snap; s != nil {
		cpu := "-"
		if s.CPUKnown {
			cpu = format.Percent(s.CPUPercent)
		}
		fmt.Fprintf(b, "  Total CPU:  %s\n", cpu)
		fmt.Fprintf(b, "  Load:       %.2f %.2f %.2f (1, 5, 15 min)\n", s.Load1, s.Load5, s.Load15)
	}
}

// writeTop lists the five busiest processes. 100% is one core.
func writeTop(b *strings.Builder, s *collector.Snapshot) {
	if s == nil {
		b.WriteString("  (no data)\n")
		return
	}
	top := collector.Top(collector.UserProcesses(s.Processes), 5)
	if len(top) == 0 {
		b.WriteString("  (no data)\n")
		return
	}
	fmt.Fprintf(b, "  %-8s %-20s %6s  %s\n", "PID", "NAME", "CPU", "ORIGIN")
	for _, p := range top {
		cpu := "-"
		if p.CPUKnown {
			cpu = format.Percent(p.CPU)
		}
		fmt.Fprintf(b, "  %-8d %-20.20s %6s  %s\n", p.PID, p.Name, cpu, p.Origin)
	}
	b.WriteString("  (100% = one core)\n")
	if s.DockerErr != nil {
		b.WriteString("  Note: Docker was not reachable, so container names may be missing.\n")
	}
}

// DescribeOrigin spells out where a process runs: container with compose
// project and image, systemd unit, or host.
func DescribeOrigin(o collector.Origin) string {
	switch o.Kind {
	case collector.KindContainer:
		name := o.ContainerName
		if name == "" {
			name = o.ContainerID[:min(len(o.ContainerID), 12)]
		}
		parts := []string{}
		if o.Project != "" {
			parts = append(parts, "compose project: "+o.Project)
		}
		if o.Service != "" {
			parts = append(parts, "service: "+o.Service)
		}
		if o.Image != "" {
			parts = append(parts, "image: "+o.Image)
		}
		if len(parts) == 0 {
			return "container " + name
		}
		return fmt.Sprintf("container %s (%s)", name, strings.Join(parts, ", "))
	case collector.KindService:
		return "systemd unit " + o.Unit
	default:
		return "host (not in a container or systemd service)"
	}
}

// duration shortens "15m0s" to "15m" and "1h0m0s" to "1h".
func duration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
