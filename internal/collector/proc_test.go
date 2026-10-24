package collector

import (
	"os"
	"testing"
	"time"
)

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name string
		line string
		want procStat
	}{
		{
			name: "plain",
			line: "1234 (sshd) S 1 1234 1234 0 -1 4194560 100 0 0 0 150 50 0 0 20 0 1 0 98765 123456789 4321 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0",
			want: procStat{comm: "sshd", ppid: 1, cpuSeconds: 2.0, start: 98765, rssPages: 4321},
		},
		{
			name: "name with spaces and parentheses",
			line: "77 (Web (Content) x) R 5 77 77 0 -1 0 0 0 0 0 100 0 0 0 20 0 1 0 555 1 10 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 3 0 0 0 0 0",
			want: procStat{comm: "Web (Content) x", ppid: 5, cpuSeconds: 1.0, start: 555, rssPages: 10},
		},
	}
	for _, tt := range tests {
		got, err := parseProcStat([]byte(tt.line))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got != tt.want {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestParseProcStatErrors(t *testing.T) {
	for _, line := range []string{"", "1234 sshd S 1", "1234 (sshd) S 1 2 3", "1 (x) S a b c d e f g h i j k l m n o p q r s t u v w"} {
		if _, err := parseProcStat([]byte(line)); err == nil {
			t.Errorf("parseProcStat(%q): expected an error", line)
		}
	}
}

func TestParseCPULine(t *testing.T) {
	busy, total, err := parseCPULine("cpu  100 10 50 800 40 5 5 10 20 0")
	if err != nil {
		t.Fatal(err)
	}
	// total = first eight values (guest is already inside user); idle = idle + iowait
	if total != 1020 || busy != 180 {
		t.Errorf("busy/total = %v/%v, want 180/1020", busy, total)
	}
	for _, bad := range []string{"", "cpu0 1 2 3 4 5 6 7 8", "cpu 1 2 3"} {
		if _, _, err := parseCPULine(bad); err == nil {
			t.Errorf("parseCPULine(%q): expected an error", bad)
		}
	}
}

func TestCPUPercent(t *testing.T) {
	before := procTimes{start: 100, cpu: 10.0}

	got, ok := cpuPercent(before, procTimes{start: 100, cpu: 14.0}, 2.0)
	if !ok || got != 200 {
		t.Errorf("same process: got (%v, %v), want (200, true)", got, ok)
	}
	if _, ok := cpuPercent(before, procTimes{start: 999, cpu: 0.5}, 2.0); ok {
		t.Error("a reused PID (different start time) must not produce a CPU value")
	}
	if _, ok := cpuPercent(before, procTimes{start: 100, cpu: 11}, 0); ok {
		t.Error("zero elapsed time must not produce a CPU value")
	}
	if got, _ := cpuPercent(before, procTimes{start: 100, cpu: 9}, 1); got != 0 {
		t.Errorf("a counter that went backwards should give 0, got %v", got)
	}
}

func TestProcessName(t *testing.T) {
	if got := processName("sshd", []string{"/usr/sbin/sshd", "-D"}); got != "sshd" {
		t.Errorf("short name: %q", got)
	}
	// comm is cut at 15 characters; the command line has the full name
	if got := processName("systemd-resolve", []string{"/usr/lib/systemd/systemd-resolved"}); got != "systemd-resolved" {
		t.Errorf("truncated name: %q", got)
	}
	// a 15-character name that does not match the command line stays as it is
	if got := processName("Isolated Web Co", []string{"/usr/lib64/firefox/firefox"}); got != "Isolated Web Co" {
		t.Errorf("thread name: %q", got)
	}
}

// TestCollectDeltas runs the collector against the real /proc: the first
// snapshot has no CPU values, the second has them, and this test process is in it.
func TestCollectDeltas(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs Linux /proc")
	}
	t.Setenv("DOCKER_HOST", "unix:///nonexistent") // keep the test independent of Docker
	c := New()
	defer c.Close()

	first, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUKnown {
		t.Error("first snapshot must not claim a server CPU value")
	}
	for _, p := range first.Processes {
		if p.CPUKnown {
			t.Fatalf("first snapshot: process %d has a CPU value", p.PID)
		}
	}
	if first.DockerErr == nil {
		t.Error("expected DockerErr with an unreachable Docker host")
	}

	// burn a little CPU so there is something to measure
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
	}

	second, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if !second.CPUKnown {
		t.Error("second snapshot should have a server CPU value")
	}
	self := os.Getpid()
	for _, p := range second.Processes {
		if int(p.PID) == self {
			if !p.CPUKnown || p.CPU <= 0 {
				t.Errorf("own process: CPUKnown=%v CPU=%v, want a positive value", p.CPUKnown, p.CPU)
			}
			if p.RSS == 0 || p.Name == "" {
				t.Errorf("own process: missing name or memory: %+v", p)
			}
			return
		}
	}
	t.Error("test process not found in snapshot")
}
