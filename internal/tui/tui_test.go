package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"eternalshell/internal/collector"
)

func fakeSnapshot() *collector.Snapshot {
	return &collector.Snapshot{
		CPUPercent: 6, MemPercent: 33, Load1: 0.4, Load5: 0.3, Load15: 0.2,
		Mounts: []collector.Mount{{Path: "/", UsedPercent: 84}},
		Containers: []collector.Container{
			{Name: "lienks-app-app-1", Project: "lienks-app", Service: "app", State: "running", Status: "Up 2 weeks (healthy)", CPU: 12, MemBytes: 90 << 20, MemOK: true},
			{Name: "lienks-app-cron-1", Project: "lienks-app", Service: "cron", State: "exited", Status: "Exited (143) 6 months ago", Restarts: 3},
			{Name: "traefik", State: "running", Status: "Up 6 weeks", CPU: 0, MemBytes: 53 << 20, MemOK: true},
		},
		Processes: []collector.Process{
			{PID: 48211, Name: "xmrig", CPU: 382, RSS: 210 << 20,
				Origin: collector.Origin{Kind: collector.KindContainer, ContainerID: strings.Repeat("a", 64), Project: "test", Service: "old-test"}},
			{PID: 611, Name: "sshd", CPU: 0, RSS: 8 << 20,
				Origin: collector.Origin{Kind: collector.KindService, Unit: "ssh.service"}},
		},
	}
}

func TestContainerRows(t *testing.T) {
	rows := containerRows(fakeSnapshot().Containers)

	// two group headers + three containers
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5: %v", len(rows), rows)
	}
	if rows[0][0] != "lienks-app" || rows[3][0] != standaloneGroup {
		t.Errorf("group headers wrong: %q, %q", rows[0][0], rows[3][0])
	}
	if got := rows[1][0]; got != "  ● app" { // service name inside a project
		t.Errorf("row 1 name = %q", got)
	}
	if rows[1][2] != "12%" || rows[1][3] != "90.0M" {
		t.Errorf("row 1 cpu/mem = %q/%q", rows[1][2], rows[1][3])
	}
	// stopped container: no CPU/MEM, but restarts are shown
	if rows[2][0] != "  ○ cron" || rows[2][2] != "-" || rows[2][3] != "-" || rows[2][4] != "3" {
		t.Errorf("stopped row = %v", rows[2])
	}
	if got := rows[4][0]; got != "  ● traefik" {
		t.Errorf("standalone name = %q", got)
	}
}

func TestProcessRows(t *testing.T) {
	rows := processRows(fakeSnapshot().Processes)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d", len(rows))
	}
	want := []string{"48211", "xmrig", "382%", "210.0M", "container test/old-test"}
	for i, w := range want {
		if rows[0][i] != w {
			t.Errorf("rows[0][%d] = %q, want %q", i, rows[0][i], w)
		}
	}
	if rows[1][4] != "systemd: ssh.service" {
		t.Errorf("origin = %q", rows[1][4])
	}
}

// ready returns a model that has a window size and one snapshot.
func ready(t *testing.T, snap *collector.Snapshot) Model {
	t.Helper()
	m := New(nil)
	var next tea.Model = m
	next, _ = next.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	next, _ = next.Update(snapshotMsg{snap: snap})
	return next.(Model)
}

func press(m Model, key string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return next.(Model)
}

func TestViewSwitching(t *testing.T) {
	m := ready(t, fakeSnapshot())

	out := m.View()
	for _, want := range []string{"[1 Containers]", "lienks-app", "healthy", "traefik", "CPU 6%", "disk / 84%"} {
		if !strings.Contains(out, want) {
			t.Errorf("containers view missing %q:\n%s", want, out)
		}
	}

	m = press(m, "2")
	out = m.View()
	for _, want := range []string{"[2 Processes]", "ORIGIN", "xmrig", "container test/old-test", "systemd: ssh.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("processes view missing %q:\n%s", want, out)
		}
	}

	m = press(m, "1")
	if !strings.Contains(m.View(), "[1 Containers]") {
		t.Error("pressing 1 should return to containers")
	}
}

func TestDockerUnreachable(t *testing.T) {
	snap := fakeSnapshot()
	snap.Containers = nil
	snap.DockerErr = errors.New("cannot connect")

	m := ready(t, snap)
	if out := m.View(); !strings.Contains(out, "Docker is not reachable: cannot connect") {
		t.Errorf("missing docker hint:\n%s", out)
	}
	// processes still work
	m = press(m, "2")
	if !strings.Contains(m.View(), "xmrig") {
		t.Error("processes view should still show processes")
	}
}

func TestQuit(t *testing.T) {
	m := ready(t, fakeSnapshot())
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit")
	}
}

func TestTickDoesNotOverlap(t *testing.T) {
	m := ready(t, fakeSnapshot()) // loading == false after a snapshot
	next, cmd := m.Update(tickMsg{})
	if !next.(Model).loading || cmd == nil {
		t.Error("tick should start a collection")
	}
	again, _ := next.Update(tickMsg{})
	if !again.(Model).loading {
		t.Error("tick during a running collection must not reset loading")
	}
}
