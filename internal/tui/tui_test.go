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
		CPUPercent: 6, CPUKnown: true, MemPercent: 33, Load1: 0.4, Load5: 0.3, Load15: 0.2,
		Mounts: []collector.Mount{{Path: "/", UsedPercent: 84}},
		Containers: []collector.Container{
			{Name: "lienks-app-app-1", Project: "lienks-app", Service: "app", State: "running", Status: "Up 2 weeks (healthy)", CPU: 12, CPUKnown: true, MemBytes: 90 << 20, MemOK: true},
			{Name: "lienks-app-cron-1", Project: "lienks-app", Service: "cron", State: "exited", Status: "Exited (143) 6 months ago", Restarts: 3},
			{Name: "traefik", Image: "traefik:v3.1", State: "running", Status: "Up 6 weeks", CPU: 0, CPUKnown: true, MemBytes: 53 << 20, MemOK: true},
		},
		Processes: []collector.Process{
			{PID: 48211, Name: "xmrig", CPU: 382, CPUKnown: true, RSS: 210 << 20,
				Origin: collector.Origin{Kind: collector.KindContainer, ContainerID: strings.Repeat("a", 64), Project: "test", Service: "old-test"}},
			{PID: 611, Name: "sshd", CPU: 0, CPUKnown: true, RSS: 8 << 20,
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
	if rows[0].cells[0] != "lienks-app" || rows[3].cells[0] != standaloneGroup {
		t.Errorf("group headers wrong: %q, %q", rows[0].cells[0], rows[3].cells[0])
	}
	if got := rows[1].cells[0]; got != "  ● app" { // service name inside a project
		t.Errorf("row 1 name = %q", got)
	}
	if rows[1].cells[2] != "12%" || rows[1].cells[3] != "90.0M" {
		t.Errorf("row 1 cpu/mem = %q/%q", rows[1].cells[2], rows[1].cells[3])
	}
	// stopped container: no CPU/MEM, but restarts are shown
	if rows[2].cells[0] != "  ○ cron" || rows[2].cells[2] != "-" || rows[2].cells[3] != "-" || rows[2].cells[4] != "3" {
		t.Errorf("stopped row = %v", rows[2].cells)
	}
	if got := rows[4].cells[0]; got != "  ● traefik (traefik:v3.1)" {
		t.Errorf("standalone name = %q", got)
	}
}

func TestProcessRows(t *testing.T) {
	rows := processRows(fakeSnapshot().Processes, false)
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

func TestExitCode(t *testing.T) {
	tests := []struct {
		status string
		code   int
		ok     bool
	}{
		{"Exited (143) 6 months ago", 143, true},
		{"Exited (0) 3 days ago", 0, true},
		{"Up 2 weeks", 0, false},
		{"Created", 0, false},
	}
	for _, tt := range tests {
		code, ok := exitCode(tt.status)
		if code != tt.code || ok != tt.ok {
			t.Errorf("exitCode(%q) = (%d, %v), want (%d, %v)", tt.status, code, ok, tt.code, tt.ok)
		}
	}
}

func TestProblem(t *testing.T) {
	tests := []struct {
		name string
		c    collector.Container
		want bool
	}{
		{"healthy", collector.Container{State: "running", CPU: 5}, false},
		{"running with restarts", collector.Container{State: "running", Restarts: 2}, true},
		{"high CPU", collector.Container{State: "running", CPU: 80}, true},
		{"exited cleanly", collector.Container{State: "exited", Status: "Exited (0) 3 days ago"}, false},
		{"docker stop (143)", collector.Container{State: "exited", Status: "Exited (143) 6 months ago"}, false},
		{"crashed", collector.Container{State: "exited", Status: "Exited (1) 2 minutes ago"}, true},
		{"stopped, old restarts", collector.Container{State: "exited", Status: "Exited (0) 1 day ago", Restarts: 5}, false},
		{"restarting loop", collector.Container{State: "restarting"}, true},
		{"dead", collector.Container{State: "dead"}, true},
		{"created", collector.Container{State: "created", Status: "Created"}, false},
	}
	for _, tt := range tests {
		if got := problem(tt.c); got != tt.want {
			t.Errorf("%s: problem = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGroupOrder(t *testing.T) {
	cs := []collector.Container{
		{Name: "traefik", State: "running"},
		{Name: "z-ok", Project: "zeta", Service: "z", State: "running"},
		{Name: "a-ok", Project: "alpha", Service: "a", State: "running"},
		{Name: "b-ok", Project: "beta", Service: "b", State: "running"},
		{Name: "b-crash", Project: "beta", Service: "c", State: "exited", Status: "Exited (1) now"},
		{Name: "lone-crash", State: "running", Restarts: 1},
	}
	groups := groupContainers(cs)

	var names []string
	for _, g := range groups {
		names = append(names, g.name)
	}
	want := []string{"beta", "standalone", "alpha", "zeta"} // problems first, then alphabetical
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("group order = %v, want %v", names, want)
		}
	}
	if groups[0].containers[0].Name != "b-crash" {
		t.Errorf("problem container should come first in its group: %+v", groups[0].containers)
	}
	if groups[1].containers[0].Name != "lone-crash" {
		t.Errorf("standalone problem container should come first: %+v", groups[1].containers)
	}

	rows := containerRows(cs)
	if rows[0].cells[0] != "beta !" {
		t.Errorf("first header = %q, want %q", rows[0].cells[0], "beta !")
	}
}

func TestListCursorSkipsHeaders(t *testing.T) {
	l := list{cols: containerColumns(80)}
	l.setSize(80, 8)
	l.setRows([]listRow{
		{cells: []string{"A"}, header: true},
		{cells: []string{"a1"}, id: "a1"},
		{cells: []string{"a2"}, id: "a2"},
		{cells: []string{"B"}, header: true},
		{cells: []string{"b1"}, id: "b1"},
	})
	if l.selectedID() != "a1" {
		t.Fatalf("initial selection = %q, want a1 (header skipped)", l.selectedID())
	}
	l.move(1)
	l.move(1) // jumps over header B
	if l.selectedID() != "b1" {
		t.Errorf("selection = %q, want b1", l.selectedID())
	}
	l.move(1) // already last
	if l.selectedID() != "b1" {
		t.Errorf("selection moved past the end: %q", l.selectedID())
	}
	l.move(-1)
	l.move(-1)
	l.move(-1) // already first
	if l.selectedID() != "a1" {
		t.Errorf("selection = %q, want a1", l.selectedID())
	}
}

func TestListKeepsSelectionAcrossRefresh(t *testing.T) {
	l := list{cols: containerColumns(80)}
	l.setSize(80, 8)
	rows := []listRow{
		{cells: []string{"A"}, header: true},
		{cells: []string{"a1"}, id: "a1"},
		{cells: []string{"a2"}, id: "a2"},
	}
	l.setRows(rows)
	l.move(1)
	// a new group appears above; the selected container moves down
	l.setRows(append([]listRow{{cells: []string{"Z"}, header: true}, {cells: []string{"z1"}, id: "z1"}}, rows...))
	if l.selectedID() != "a2" {
		t.Errorf("selection = %q, want a2", l.selectedID())
	}
	// the selected container disappears: cursor lands on a real row
	l.setRows(rows[:2])
	if l.selectedID() == "" {
		t.Error("cursor should land on a container when the selected one vanishes")
	}
}

func TestListScrollKeepsCursorVisible(t *testing.T) {
	l := list{cols: containerColumns(80)}
	l.setSize(80, 5) // 3 body lines
	var rows []listRow
	for i := 0; i < 10; i++ {
		rows = append(rows, listRow{cells: []string{"c"}, id: string(rune('a' + i))})
	}
	l.setRows(rows)
	for i := 0; i < 7; i++ {
		l.move(1)
	}
	if l.cursor < l.offset || l.cursor >= l.offset+3 {
		t.Errorf("cursor %d outside window [%d,%d)", l.cursor, l.offset, l.offset+3)
	}
	if n := strings.Count(l.view(), "\n") + 1; n != 5 {
		t.Errorf("view has %d lines, want 5", n)
	}
}

func TestKernelThreadToggle(t *testing.T) {
	snap := fakeSnapshot()
	snap.Processes = append(snap.Processes, collector.Process{PID: 77, Name: "kworker/0:1", Kernel: true})
	m := ready(t, snap)
	m = press(m, "2")

	if strings.Contains(m.View(), "kworker") {
		t.Error("kernel threads should be hidden by default")
	}
	if !strings.Contains(m.View(), "kernel threads (hidden)") {
		t.Error("footer should say kernel threads are hidden")
	}

	m = press(m, "k")
	if !strings.Contains(m.View(), "kworker") || !strings.Contains(m.View(), "kernel threads (shown)") {
		t.Errorf("k should show kernel threads:\n%s", m.View())
	}

	m = press(m, "k")
	if strings.Contains(m.View(), "kworker") {
		t.Error("second k should hide them again")
	}

	// k does nothing in the containers view
	m = press(m, "1")
	m = press(m, "k")
	if m.showKernel {
		t.Error("k must only act in the processes view")
	}
}

func TestUnknownCPUShowsDash(t *testing.T) {
	snap := fakeSnapshot()
	snap.CPUKnown = false
	for i := range snap.Containers {
		snap.Containers[i].CPUKnown = false
	}
	for i := range snap.Processes {
		snap.Processes[i].CPUKnown = false
	}

	m := ready(t, snap)
	if !strings.Contains(m.View(), "CPU -") {
		t.Errorf("header should show CPU -:\n%s", m.View())
	}
	rows := containerRows(snap.Containers)
	if rows[1].cells[2] != "-" {
		t.Errorf("running container CPU = %q, want -", rows[1].cells[2])
	}
	if got := processRows(snap.Processes, false)[0][2]; got != "-" {
		t.Errorf("process CPU = %q, want -", got)
	}
}
