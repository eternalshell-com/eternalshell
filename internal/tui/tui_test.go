package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"eternalshell/internal/collector"
)

func fakeSnapshot() *collector.Snapshot {
	return &collector.Snapshot{
		CPUPercent: 6, CPUKnown: true, MemPercent: 33, Load1: 0.4, Load5: 0.3, Load15: 0.2,
		Mounts: []collector.Mount{{Path: "/", UsedPercent: 84}},
		Containers: []collector.Container{
			{ID: "id-app", Name: "lienks-app-app-1", Project: "lienks-app", Service: "app", State: "running", Status: "Up 2 weeks (healthy)", CPU: 12, CPUKnown: true, MemBytes: 90 << 20, MemOK: true},
			{ID: "id-cron", Name: "lienks-app-cron-1", Project: "lienks-app", Service: "cron", State: "exited", Status: "Exited (143) 6 months ago", Restarts: 3},
			{ID: "id-traefik", Name: "traefik", Image: "traefik:v3.1", State: "running", Status: "Up 6 weeks", CPU: 0, CPUKnown: true, MemBytes: 53 << 20, MemOK: true},
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
	rows := containerRows(fakeSnapshot().Containers, nil, nil, nil)

	// two group headers + three containers
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5: %v", len(rows), rows)
	}
	if rows[0].cells[colName] != "lienks-app (2)" || rows[3].cells[colName] != standaloneGroup+" (1)" {
		t.Errorf("group headers wrong (name and container count): %q, %q", rows[0].cells[colName], rows[3].cells[colName])
	}
	if got := rows[1].cells[colName]; got != "app" { // service name inside a project
		t.Errorf("row 1 name = %q", got)
	}
	if got := rows[1].cells[colDot]; got != "●✓" { // running and healthy
		t.Errorf("row 1 dot = %q, want ●✓", got)
	}
	if rows[1].cells[colCPU] != "12%" || rows[1].cells[colMem] != "90.0M" {
		t.Errorf("row 1 cpu/mem = %q/%q", rows[1].cells[colCPU], rows[1].cells[colMem])
	}
	// stopped container: no CPU/MEM, but restarts are shown
	if rows[2].cells[colName] != "cron" || rows[2].cells[colDot] != "○ " ||
		rows[2].cells[colCPU] != "-" || rows[2].cells[colMem] != "-" || rows[2].cells[colRestarts] != "3" {
		t.Errorf("stopped row = %q", rows[2].cells)
	}
	if got := rows[4].cells[colName]; got != "traefik (traefik:v3.1)" {
		t.Errorf("standalone name = %q", got)
	}
}

func TestProcessRows(t *testing.T) {
	rows := processRows(fakeSnapshot().Processes, false, nil, false)
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
	for _, want := range []string{"[1 Containers]", "lienks-app", "healthy", "traefik", "CPU ▕", "  6%", "DISK ▕", " 84%"} {
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

	rows := containerRows(cs, nil, nil, nil)
	if rows[0].cells[colName] != "beta (2) !" {
		t.Errorf("first header = %q, want %q", rows[0].cells[colName], "beta (2) !")
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
	if !strings.Contains(m.View(), "▏   -") { // an empty bar and a dash
		t.Errorf("header should show an unknown CPU as an empty bar and a dash:\n%s", m.View())
	}
	rows := containerRows(snap.Containers, nil, nil, nil)
	if rows[1].cells[colCPU] != "-" {
		t.Errorf("running container CPU = %q, want -", rows[1].cells[colCPU])
	}
	if got := processRows(snap.Processes, false, nil, false)[0][2]; got != "-" {
		t.Errorf("process CPU = %q, want -", got)
	}
}

func enter(m Model) (Model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next.(Model), cmd
}

func send(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func logLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "2026-10-24T10:00:%02dZ line %d\n", i%60, i)
	}
	return b.String()
}

func TestEnterOpensLogs(t *testing.T) {
	m := ready(t, fakeSnapshot())

	m, cmd := enter(m)
	if !m.logs.active || m.logs.id != "id-app" {
		t.Fatalf("logs not opened for the first container: %+v", m.logs)
	}
	if cmd == nil {
		t.Error("opening the logs should start a fetch command")
	}
	out := m.View()
	for _, want := range []string{"Logs · lienks-app-app-1", "Loading logs", "esc back"} {
		if !strings.Contains(out, want) {
			t.Errorf("loading screen missing %q:\n%s", want, out)
		}
	}

	m = send(m, logsMsg{id: "id-app", text: logLines(5)})
	out = m.View()
	for _, want := range []string{"line 1", "line 5", "reload"} {
		if !strings.Contains(out, want) {
			t.Errorf("log screen missing %q:\n%s", want, out)
		}
	}
	if m.logs.loading {
		t.Error("loading should be over")
	}
}

func TestLogsShowNewestAtBottomAndScroll(t *testing.T) {
	m := ready(t, fakeSnapshot()) // 30 rows high: about 27 lines for the log
	m, _ = enter(m)
	m = send(m, logsMsg{id: "id-app", text: logLines(100)})

	out := m.View()
	if !strings.Contains(out, "line 100") || strings.Contains(out, "line 1\n") {
		t.Errorf("should start at the bottom:\n%s", out)
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyHome})
	if out := m.View(); !strings.Contains(out, "line 1") || strings.Contains(out, "line 100") {
		t.Errorf("home should jump to the top:\n%s", out)
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(m.View(), "line 100") {
		t.Error("end should jump back to the bottom")
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyUp})
	if strings.Count(m.View(), "\n")+1 != 30 {
		t.Errorf("screen should always be exactly the window height, got %d lines", strings.Count(m.View(), "\n")+1)
	}
}

func TestLogsEscGoesBack(t *testing.T) {
	m := ready(t, fakeSnapshot())
	m, _ = enter(m)
	m = send(m, logsMsg{id: "id-app", text: "hello\n"})
	m = send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.logs.active {
		t.Fatal("esc should close the logs")
	}
	out := m.View()
	if !strings.Contains(out, "[1 Containers]") || strings.Contains(out, "hello") {
		t.Errorf("should be back on the containers view:\n%s", out)
	}
}

func TestLogsReload(t *testing.T) {
	m := ready(t, fakeSnapshot())
	m, _ = enter(m)
	m = send(m, logsMsg{id: "id-app", text: "old\n"})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = next.(Model)
	if !m.logs.loading || cmd == nil {
		t.Fatal("r should start a reload")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); cmd != nil {
		t.Error("r during a running reload must not start another")
	}

	m = send(m, logsMsg{id: "id-app", text: "new\n"})
	if out := m.View(); !strings.Contains(out, "new") || strings.Contains(out, "old") {
		t.Errorf("reload should replace the text:\n%s", out)
	}
}

func TestLogsStaleResultIgnored(t *testing.T) {
	m := ready(t, fakeSnapshot())
	m, _ = enter(m)
	m = send(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = send(m, logsMsg{id: "id-app", text: "late\n"}) // arrives after going back
	if m.logs.active || m.logs.text != "" {
		t.Errorf("stale result changed the state: %+v", m.logs)
	}

	m, _ = enter(m)
	m = send(m, logsMsg{id: "some-other", text: "wrong\n"})
	if m.logs.text != "" {
		t.Error("a result for another container must be ignored")
	}
}

func TestLogsErrorAndEmpty(t *testing.T) {
	m := ready(t, fakeSnapshot())
	m, _ = enter(m)
	m = send(m, logsMsg{id: "id-app", err: errors.New("permission denied")})
	if !strings.Contains(m.View(), "Could not read the logs: permission denied") {
		t.Errorf("error not shown:\n%s", m.View())
	}

	m = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = send(m, logsMsg{id: "id-app", text: ""})
	if !strings.Contains(m.View(), "(no log output)") {
		t.Errorf("empty logs should say so:\n%s", m.View())
	}
}

func TestLogsKeysElsewhere(t *testing.T) {
	m := ready(t, fakeSnapshot())

	// Enter does nothing in the processes view
	p := press(m, "2")
	if p, _ = enter(p); p.logs.active {
		t.Error("enter must not open logs in the processes view")
	}

	// Enter does nothing without containers
	empty := fakeSnapshot()
	empty.Containers = nil
	e := ready(t, empty)
	if e, _ = enter(e); e.logs.active {
		t.Error("enter must not open logs without containers")
	}

	// q quits from the log screen
	m, _ = enter(m)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q should return a command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q should quit from the log screen")
	}

	// 2 leaves the log screen for the processes view
	m = press(m, "2")
	if m.logs.active || !strings.Contains(m.View(), "[2 Processes]") {
		t.Error("2 should leave the logs and show processes")
	}
}

func TestLogsSurviveRefreshAndResize(t *testing.T) {
	m := ready(t, fakeSnapshot())
	m, _ = enter(m)
	m = send(m, logsMsg{id: "id-app", text: "keep me\n"})

	m = send(m, snapshotMsg{snap: fakeSnapshot()}) // background refresh
	m = send(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	if !m.logs.active || !strings.Contains(m.View(), "keep me") {
		t.Errorf("logs should survive refresh and resize:\n%s", m.View())
	}
}

func TestFooterMentionsLogs(t *testing.T) {
	m := ready(t, fakeSnapshot())
	if !strings.Contains(m.View(), "1/2/3 views · ↑/↓ scroll · ↵ logs · q quit") {
		t.Errorf("footer:\n%s", m.View())
	}
}

func diskFixture() *collector.DiskUsage {
	return &collector.DiskUsage{
		Images: 12 << 30, BuildCache: 4 << 30, Reclaimable: 2 << 30,
		ByContainer: map[string]collector.DiskParts{
			"id-app":  {Layers: 100 << 20, Volumes: 900 << 20}, // 1.0G, complete
			"id-cron": {Layers: 10 << 20, LogsUnknown: true},   // incomplete logs
			// id-traefik is missing: started after the last refresh
		},
	}
}

func TestDiskColumn(t *testing.T) {
	cs := fakeSnapshot().Containers

	// before the first result: "…"; failed: "-"
	rows := containerRows(cs, nil, nil, nil)
	if rows[1].cells[colDisk] != "…" || rows[0].cells[colDisk] != "…" {
		t.Errorf("before the first result: container %q, project %q, want …", rows[1].cells[colDisk], rows[0].cells[colDisk])
	}
	if rows := containerRows(cs, nil, errors.New("down"), nil); rows[1].cells[colDisk] != "-" {
		t.Errorf("failed refresh: %q, want -", rows[1].cells[colDisk])
	}

	rows = containerRows(cs, diskFixture(), nil, nil)
	// project "lienks-app": app 1.0G + cron 10M and unknown logs -> 1.0G+? on the header
	var appRow, cronRow, headerRow, traefikRow listRow
	for _, r := range rows {
		switch {
		case r.id == "id-app":
			appRow = r
		case r.id == "id-cron":
			cronRow = r
		case r.id == "id-traefik":
			traefikRow = r
		case r.header && strings.HasPrefix(r.cells[colName], "lienks-app"):
			headerRow = r
		}
	}
	if appRow.cells[colDisk] != "1000.0M" && appRow.cells[colDisk] != "1.0G" {
		t.Errorf("app disk = %q", appRow.cells[colDisk])
	}
	if cronRow.cells[colDisk] != "10.0M+?" {
		t.Errorf("cron disk = %q, want 10.0M+? (logs unknown)", cronRow.cells[colDisk])
	}
	if !strings.HasSuffix(headerRow.cells[colDisk], "+?") {
		t.Errorf("the project row is the total of its containers and inherits the unknown: %q", headerRow.cells[colDisk])
	}
	if traefikRow.cells[colDisk] != "…" {
		t.Errorf("a container missing from the last refresh shows …, got %q", traefikRow.cells[colDisk])
	}
}

func headerLine(view string) string {
	return strings.TrimRight(strings.Split(view, "\n")[0], " ")
}

func TestDiskSummaryInTheHeader(t *testing.T) {
	// no result yet: nothing about images, and no summary line under the table
	m := ready(t, fakeSnapshot())
	out := m.View()
	if strings.Contains(headerLine(out), "images") || strings.Contains(out, "measuring Docker disk usage") {
		t.Errorf("before the first result there is no summary:\n%s", out)
	}

	// a result and a wide window: all three parts after the DISK bar
	snap := fakeSnapshot()
	snap.Disk = diskFixture()
	m = send(ready(t, snap), tea.WindowSizeMsg{Width: 200, Height: 30})
	head := headerLine(m.View())
	for _, want := range []string{"DISK ▕", "images 12.0G", "build cache 4.0G", "reclaimable 2.0G"} {
		if !strings.Contains(head, want) {
			t.Errorf("header should contain %q:\n%s", want, head)
		}
	}
	if strings.Index(head, "DISK") > strings.Index(head, "images") {
		t.Errorf("the summary comes after the DISK bar:\n%s", head)
	}
	if strings.Contains(m.View(), "disk: images") {
		t.Errorf("the summary line under the table is gone:\n%s", m.View())
	}

	// build cache and reclaimable are only shown when above 0
	snap = fakeSnapshot()
	snap.Disk = diskFixture()
	snap.Disk.BuildCache, snap.Disk.Reclaimable = 0, 0
	m = send(ready(t, snap), tea.WindowSizeMsg{Width: 200, Height: 30})
	head = headerLine(m.View())
	if !strings.Contains(head, "images 12.0G") || strings.Contains(head, "build cache") || strings.Contains(head, "reclaimable") {
		t.Errorf("only images when the rest is 0:\n%s", head)
	}

	// a failed refresh shows nothing in the header; the table says "-"
	snap = fakeSnapshot()
	snap.DiskErr = errors.New("docker not reachable")
	m = ready(t, snap)
	if strings.Contains(headerLine(m.View()), "images") {
		t.Errorf("no images figure without a result:\n%s", m.View())
	}
}

func TestHeaderFitsNarrowTerminals(t *testing.T) {
	snap := fakeSnapshot()
	snap.Disk = diskFixture()
	for _, width := range []int{200, 120, 100, 80, 70} {
		m := send(ready(t, snap), tea.WindowSizeMsg{Width: width, Height: 30})
		head := headerLine(m.View())
		if n := utf8.RuneCountInString(head); n > width {
			t.Errorf("width %d: header has %d cells:\n%s", width, n, head)
		}
		if width >= 80 && !strings.Contains(head, "images 12.0G") {
			t.Errorf("width %d: images should still fit:\n%s", width, head)
		}
	}
	// at 80 columns the extras and the load are gone, images are not
	m := send(ready(t, snap), tea.WindowSizeMsg{Width: 80, Height: 30})
	head := headerLine(m.View())
	if strings.Contains(head, "build cache") || strings.Contains(head, "reclaimable") || strings.Contains(head, "load") {
		t.Errorf("at 80 columns only the essentials fit:\n%s", head)
	}
}

func TestGaugeFormat(t *testing.T) {
	tests := []struct {
		percent float64
		known   bool
		want    string
	}{
		{12, true, "CPU ▕█░░░░░░░▏ 12%"}, // exactly one space after the bar
		{4, true, "CPU ▕░░░░░░░░▏  4%"},
		{100, true, "CPU ▕████████▏100%"},
		{0, false, "CPU ▕░░░░░░░░▏   -"},
	}
	for _, tt := range tests {
		got := gauge("CPU", tt.percent, tt.known, 8)
		if got != tt.want {
			t.Errorf("gauge(%v, %v) = %q, want %q", tt.percent, tt.known, got, tt.want)
		}
		if n := utf8.RuneCountInString(got); n != utf8.RuneCountInString(tt.want) || n != 18 {
			t.Errorf("gauge(%v) is %d cells wide; it must be the same for every value", tt.percent, n)
		}
	}
}

func TestSpacingAndSummaryLines(t *testing.T) {
	m := ready(t, fakeSnapshot())
	lines := strings.Split(m.View(), "\n")

	if strings.TrimSpace(lines[1]) != "" || !strings.Contains(lines[2], "Containers") {
		t.Errorf("an empty line between the header and the tabs:\n%s", strings.Join(lines[:4], "\n"))
	}
	if len(lines) != 30 {
		t.Errorf("containers view: %d lines, want 30", len(lines))
	}
	// the last lines are the bottom edge of the box and the footer: no line in between
	if !strings.HasPrefix(lines[len(lines)-2], "╰") || !strings.Contains(lines[len(lines)-1], "views") {
		t.Errorf("the table is followed directly by the footer:\n%s", strings.Join(lines[len(lines)-3:], "\n"))
	}

	// the processes view also has no line under the table
	p := press(m, "2")
	if pl := strings.Split(p.View(), "\n"); len(pl) != 30 || !strings.HasPrefix(pl[len(pl)-2], "╰") {
		t.Errorf("processes view: %d lines, last but one %q", len(pl), pl[len(pl)-2])
	}

	// the Domains view keeps its summary line under the box
	d, _ := m.openDomains()
	dl := strings.Split(d.(Model).View(), "\n")
	if len(dl) != 30 || !strings.HasPrefix(dl[len(dl)-3], "╰") || !strings.Contains(dl[len(dl)-2], "domains:") {
		t.Errorf("domains view: %d lines:\n%s", len(dl), strings.Join(dl[len(dl)-4:], "\n"))
	}
}

func TestProjectRowIncludesFolderFiles(t *testing.T) {
	cs := []collector.Container{
		{ID: "id-a", Name: "p-a-1", Project: "p", Service: "a", State: "running", Status: "Up 1 hour"},
	}
	disk := &collector.DiskUsage{
		ByContainer: map[string]collector.DiskParts{"id-a": {Layers: 100 << 20}},
		Projects: []collector.ProjectDisk{
			{Name: "p", DiskParts: collector.DiskParts{Layers: 100 << 20, Files: 900 << 20}},
		},
	}

	rows := containerRows(cs, disk, nil, nil)
	if rows[0].cells[colDisk] != "1000.0M" && rows[0].cells[colDisk] != "1.0G" {
		t.Errorf("the project row is containers + folder (100M + 900M): %q", rows[0].cells[colDisk])
	}
	if rows[1].cells[colDisk] != "100.0M" {
		t.Errorf("the container row has no files: %q", rows[1].cells[colDisk])
	}

	disk.Projects[0].FilesUnknown = true
	if got := containerRows(cs, disk, nil, nil)[0].cells[colDisk]; !strings.HasSuffix(got, "+?") {
		t.Errorf("an unknown folder size makes the project total incomplete: %q", got)
	}

	// standalone containers have no project folder
	solo := []collector.Container{{ID: "id-s", Name: "solo", State: "running"}}
	d2 := &collector.DiskUsage{ByContainer: map[string]collector.DiskParts{"id-s": {Layers: 1 << 20}}}
	if got := containerRows(solo, d2, nil, nil)[0].cells[colDisk]; got != "1.0M" {
		t.Errorf("standalone header = %q, want 1.0M", got)
	}
}
