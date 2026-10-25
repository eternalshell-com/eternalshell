package watch

import (
	"strings"
	"testing"
	"time"

	"eternalshell/internal/collector"
)

func testDetails() (Details, collector.Process) {
	xmrig := collector.Process{
		PID: 48211, Name: "xmrig", CPU: 382, CPUKnown: true,
		Cmdline: "/tmp/xmrig -o pool.example.com", User: "root",
		Origin: collector.Origin{
			Kind: collector.KindContainer, ContainerID: strings.Repeat("a", 64),
			ContainerName: "old-test", Project: "test", Service: "miner", Image: "node:16",
		},
	}
	ssh := collector.Process{PID: 611, Name: "sshd", CPU: 1, CPUKnown: true,
		Origin: collector.Origin{Kind: collector.KindService, Unit: "ssh.service"}}
	d := Details{
		Server: "main-vps",
		Now:    time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
		Proc:   &xmrig,
		Snap: &collector.Snapshot{
			CPUPercent: 97, CPUKnown: true, Load1: 6.4, Load5: 5.1, Load15: 3.2,
			Processes: []collector.Process{ssh, xmrig},
		},
	}
	return d, xmrig
}

func TestProcessAlertMessage(t *testing.T) {
	d, _ := testDetails()
	n := Notice{Kind: KindAlert, Key: Key{Rule: RuleProcess, PID: 48211}, Avg: 382, Threshold: 80, Window: 15 * time.Minute}

	m := Message(n, d)

	if m.Subject != "[EternalShell] main-vps - xmrig (PID 48211) at 382% CPU for 15m" {
		t.Errorf("subject = %q", m.Subject)
	}
	for _, want := range []string{
		"382% of one core", "last 15m", "Threshold: 80%",
		"Command:  /tmp/xmrig -o pool.example.com", "User:     root",
		"container old-test (compose project: test, service: miner, image: node:16)",
		"docker logs old-test",
		"Total CPU:  97%", "Load:       6.40 5.10 3.20",
		"sshd", "systemd: ssh.service", // the top list names every origin
		"one all-clear mail",
	} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body should contain %q:\n%s", want, m.Body)
		}
	}
	if strings.Index(m.Body, "xmrig  ") > strings.Index(m.Body, "sshd") {
		t.Errorf("top list should be sorted by CPU:\n%s", m.Body)
	}
}

func TestTotalAlertMessage(t *testing.T) {
	d, _ := testDetails()
	d.Proc = nil
	n := Notice{Kind: KindAlert, Key: Key{Rule: RuleTotal}, Avg: 95, Threshold: 90, Window: 10 * time.Minute}

	m := Message(n, d)

	if m.Subject != "[EternalShell] main-vps - server CPU at 95% for 10m" {
		t.Errorf("subject = %q", m.Subject)
	}
	for _, want := range []string{"whole server", "Likely cause", "xmrig", "container test/miner"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body should contain %q:\n%s", want, m.Body)
		}
	}
}

func TestClearMessages(t *testing.T) {
	d, _ := testDetails()
	base := Notice{Kind: KindClear, Key: Key{Rule: RuleProcess, PID: 48211}, Avg: 12, Threshold: 80, Window: 15 * time.Minute, Lasted: 40 * time.Minute}

	below := Message(base, d)
	if !strings.Contains(below.Subject, "all clear") || !strings.Contains(below.Subject, "calm again") {
		t.Errorf("subject = %q", below.Subject)
	}
	if !strings.Contains(below.Body, "back below 80%") || !strings.Contains(below.Body, "about 40m") {
		t.Errorf("body:\n%s", below.Body)
	}

	exited := base
	exited.Reason = ReasonExited
	if m := Message(exited, d); !strings.Contains(m.Subject, "has exited") {
		t.Errorf("subject = %q", m.Subject)
	}

	// a process that was never seen still gets a sensible mail
	d.Proc = nil
	if m := Message(exited, d); !strings.Contains(m.Subject, "unknown process (PID 48211)") {
		t.Errorf("subject = %q", m.Subject)
	}
}

func TestDescribeOrigin(t *testing.T) {
	tests := []struct {
		o    collector.Origin
		want string
	}{
		{collector.Origin{Kind: collector.KindHost}, "host"},
		{collector.Origin{Kind: collector.KindService, Unit: "docker.service"}, "systemd unit docker.service"},
		{collector.Origin{Kind: collector.KindContainer, ContainerID: strings.Repeat("b", 64)}, "container bbbbbbbbbbbb"},
		{collector.Origin{Kind: collector.KindContainer, ContainerName: "web", Image: "nginx"}, "container web (image: nginx)"},
	}
	for _, tt := range tests {
		if got := DescribeOrigin(tt.o); !strings.HasPrefix(got, tt.want) {
			t.Errorf("DescribeOrigin = %q, want prefix %q", got, tt.want)
		}
	}
}

func TestDuration(t *testing.T) {
	tests := map[time.Duration]string{
		15 * time.Minute:                "15m",
		time.Hour:                       "1h",
		90 * time.Minute:                "1h30m",
		30 * time.Second:                "30s",
		10*time.Minute + 30*time.Second: "10m30s",
	}
	for d, want := range tests {
		if got := duration(d); got != want {
			t.Errorf("duration(%v) = %q, want %q", d, got, want)
		}
	}
}
