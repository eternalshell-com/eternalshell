// Package tui is the live terminal UI, built with Bubble Tea.
//
// Bubble Tea follows the "Elm architecture": a Model holds all state, Update
// receives messages (key presses, timer ticks, results) and returns the next
// Model, and View turns the Model into the text shown on screen.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"eternalshell/internal/collector"
	"eternalshell/internal/format"
)

// refreshEvery is the time between two snapshots. CPU values are averages over
// this interval.
const refreshEvery = 2 * time.Second

// chromeLines are the screen rows around the table of the three main views: the
// info line, the tabs, the top and bottom edge of the border, the summary line
// and the footer. The log screen has no tabs, border or summary line.
const (
	chromeLines     = 6
	logsChromeLines = 3
)

// logLinesShown is the number of log lines requested; shown in the log title.
const logLinesShown = 200

type view int

const (
	viewContainers view = iota
	viewProcesses
	viewDomains
)

// Messages that Update can receive besides key presses.
type (
	tickMsg     time.Time
	snapshotMsg struct {
		snap *collector.Snapshot
		err  error
	}
)

// Model is the whole UI state.
type Model struct {
	col  *collector.Collector
	host string

	view    view
	snap    *collector.Snapshot // latest successful snapshot, nil before the first
	err     error               // error of the latest failed refresh
	loading bool                // a collection is running; do not start another

	showKernel bool // processes view: include kernel threads
	logs       logsState
	domains    domainsState

	width, height int
	containers    list
	processes     table.Model
	domainList    list

	// Recent CPU samples for the trend columns, about a minute each, newest last.
	history  map[string][]float64 // by container ID
	procHist map[procKey][]float64
}

// New creates the UI. Collection starts when the program runs (Init).
func New(col *collector.Collector) Model {
	host, _ := os.Hostname()
	newTable := func() table.Model {
		return table.New(table.WithFocused(true), table.WithStyles(tableStyles()))
	}
	m := Model{
		col:       col,
		host:      host,
		loading:   true,
		width:     80,
		height:    24,
		processes: newTable(),
		history:   map[string][]float64{},
		procHist:  map[procKey][]float64{},
	}
	m.layout()
	return m
}

// Init runs once at start: collect right away and start the refresh timer.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.collect(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// collect returns a command: a function Bubble Tea runs in the background.
// The UI stays responsive while it measures, and the result arrives as a
// snapshotMsg.
func (m Model) collect() tea.Cmd {
	col := m.col
	return func() tea.Msg {
		snap, err := col.Collect()
		return snapshotMsg{snap: snap, err: err}
	}
}

// Update handles one message and returns the new model plus an optional command.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.logs.active {
			return m.updateLogsKey(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.view == viewContainers {
				return m.openLogs()
			}
		case "1":
			m.view = viewContainers
			return m, nil
		case "2":
			m.view = viewProcesses
			return m, nil
		case "3":
			return m.openDomains()
		case "k":
			if m.view == viewProcesses {
				m.showKernel = !m.showKernel
				if m.snap != nil {
					m.refreshRows()
				}
				return m, nil
			}
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.logs.active {
			m.layoutLogs()
		}
		return m, nil

	case logsMsg:
		return m.handleLogsMsg(msg)

	case domainsMsg:
		m.domains.running = false
		m.domains.results, m.domains.checkedAt = msg.results, msg.at
		m.refreshDomainRows()
		return m, nil

	case recheckMsg:
		// The timer keeps running; a check only starts while the view is shown.
		cmds := []tea.Cmd{scheduleRecheck()}
		if m.view == viewDomains {
			cmds = append(cmds, m.startDomainCheck(time.Now()))
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		// The timer always keeps running; a new collection only starts if the
		// previous one has finished (normally it is done within milliseconds).
		if m.loading {
			return m, tick()
		}
		m.loading = true
		return m, tea.Batch(tick(), m.collect())

	case snapshotMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err, m.snap = nil, msg.snap
		m.recordHistory(msg.snap)
		m.refreshRows()
		return m, nil
	}

	// Anything else (arrow keys, page up/down) scrolls the visible table.
	if l := m.activeList(); l != nil {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "up":
				l.move(-1)
			case "down":
				l.move(1)
			case "pgup":
				l.page(-1)
			case "pgdown":
				l.page(1)
			case "home":
				l.top()
			case "end":
				l.bottom()
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.processes, cmd = m.processes.Update(msg)
	return m, cmd
}

// activeList returns the list of the current view, or nil for the processes
// view, which uses the bubbles table.
func (m *Model) activeList() *list {
	switch m.view {
	case viewContainers:
		return &m.containers
	case viewDomains:
		return &m.domainList
	}
	return nil
}

// innerWidth is the width inside the border.
func (m Model) innerWidth() int { return max(m.width-2, 20) }

// tableHeight is the height of the table inside the border.
func (m Model) tableHeight() int { return max(m.height-chromeLines, 4) }

// layout sizes the tables to the terminal.
func (m *Model) layout() {
	w, h := m.innerWidth(), m.tableHeight()
	m.containers.cols = containerColumns(w)
	m.containers.setSize(w, h)
	m.domainList.cols = domainColumns(w, domProjectMn) // refreshDomainRows sizes PROJECT to the names
	m.domainList.setSize(w, h)

	// The rows of the bubbles table must match its columns, and the columns can
	// change with the width (the trend column), so the rows are cleared first and
	// built again below.
	m.processes.SetRows(nil)
	m.processes.SetColumns(processColumns(w))
	m.processes.SetHeight(h)
	m.processes.SetWidth(w)
	if m.snap != nil {
		m.refreshRows()
	}
}

func (m *Model) refreshRows() {
	m.containers.setRows(containerRows(m.snap.Containers, m.snap.Disk, m.snap.DiskErr, m.history))
	m.processes.SetRows(processRows(m.snap.Processes, m.showKernel, m.procHist, withTrend(m.innerWidth())))
	m.refreshDomainRows()
	// Rows can shrink between refreshes; re-set the cursor so it is clamped.
	m.processes.SetCursor(m.processes.Cursor())
}

// recordHistory adds the CPU of this snapshot to the trend histories and
// forgets what is gone. Samples that are not known yet (the first snapshot) are
// left out.
func (m *Model) recordHistory(snap *collector.Snapshot) {
	live := map[string]bool{}
	for _, c := range snap.Containers {
		if !c.Running() {
			continue
		}
		live[c.ID] = true
		if c.CPUKnown {
			m.history[c.ID] = appendSample(m.history[c.ID], c.CPU)
		}
	}
	for id := range m.history {
		if !live[id] {
			delete(m.history, id)
		}
	}

	seen := map[procKey]bool{}
	for _, p := range snap.Processes {
		if p.Kernel {
			continue
		}
		k := procKey{p.PID, p.Start}
		seen[k] = true
		if p.CPUKnown {
			m.procHist[k] = appendSample(m.procHist[k], p.CPU)
		}
	}
	for k := range m.procHist {
		if !seen[k] {
			delete(m.procHist, k)
		}
	}
}

// View renders the whole screen.
func (m Model) View() string {
	if m.logs.active {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.infoView(),
			m.logsTitleView(),
			m.logsBodyView(),
			m.logsFooterView(),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.infoView(),
		m.tabsView(),
		box(m.viewName(), m.bodyView(), m.innerWidth()),
		m.diskSummaryView(),
		m.footerView(),
	)
}

func (m Model) viewName() string {
	switch m.view {
	case viewProcesses:
		return "Processes"
	case viewDomains:
		return "Domains"
	}
	return "Containers"
}

// box draws a rounded border around the content with the title in the top
// edge. Every content line is cut or padded to innerW, so the right edge stays
// straight. (lipgloss has no border titles, hence the hand-made box.)
func box(title, content string, innerW int) string {
	edge := borderStyle
	fill := max(innerW-3-lipgloss.Width(title), 0)
	lines := []string{
		edge.Render("╭─ ") + titleStyle.Render(title) + edge.Render(" "+strings.Repeat("─", fill)+"╮"),
	}
	for _, line := range strings.Split(content, "\n") {
		line = lipgloss.NewStyle().MaxWidth(innerW).Render(line)
		line += strings.Repeat(" ", max(innerW-lipgloss.Width(line), 0))
		lines = append(lines, edge.Render("│")+line+edge.Render("│"))
	}
	lines = append(lines, edge.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return strings.Join(lines, "\n")
}

// infoView is the header: the title and three usage bars. It picks the widest
// variant that fits the terminal: first the bars get narrower, then the load
// and the host name are dropped.
func (m Model) infoView() string {
	if m.snap == nil {
		s := " " + titleStyle.Render("EternalShell")
		if m.host != "" {
			s += infoStyle.Render(" · " + m.host)
		}
		return s + infoStyle.Render(" · collecting…")
	}
	variants := []struct {
		bar        int
		host, load bool
	}{
		{10, true, true}, {8, true, true}, {6, true, true}, {6, false, true}, {6, false, false}, {4, false, false},
	}
	var line string
	for _, v := range variants {
		line = m.headerLine(v.bar, v.host, v.load)
		if lipgloss.Width(line) <= m.width {
			return line
		}
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(line)
}

func (m Model) headerLine(barW int, host, load bool) string {
	sep := infoStyle.Render(" · ")
	s := " " + titleStyle.Render("EternalShell")
	if host && m.host != "" {
		s += sep + infoStyle.Render(m.host)
	}
	n := m.snap
	s += sep + gauge("CPU", n.CPUPercent, n.CPUKnown, barW)
	s += sep + gauge("MEM", n.MemPercent, true, barW)
	if root := rootMount(n.Mounts); root != nil {
		s += sep + gauge("DISK", root.UsedPercent, true, barW)
	}
	if load {
		s += sep + infoStyle.Render(fmt.Sprintf("load %.2f %.2f %.2f", n.Load1, n.Load5, n.Load15))
	}
	return s
}

// gauge draws "CPU ▕███░░░░░▏ 12%". The filled part has the color of the level
// (green, yellow, red); an unknown value is an empty bar and a dash.
func gauge(label string, percent float64, known bool, width int) string {
	if !known {
		percent = 0
	}
	filled, empty := bar(percent, width)
	text := "-"
	if known {
		text = format.Percent(percent)
	}
	return infoStyle.Render(label+" ▕") +
		levelColor(barLevel(percent)).Render(filled) + dimStyle.Render(empty) +
		infoStyle.Render("▏ "+fmt.Sprintf("%4s", text))
}

// rootMount returns the "/" mount, or the first mount if there is no "/".
func rootMount(ms []collector.Mount) *collector.Mount {
	for i := range ms {
		if ms[i].Path == "/" {
			return &ms[i]
		}
	}
	if len(ms) > 0 {
		return &ms[0]
	}
	return nil
}

// tabsView: the active tab has an accent background, the others are dim. Without
// colors the active tab gets brackets instead; the width is the same.
func (m Model) tabsView() string {
	tab := func(label string, v view) string {
		switch {
		case m.view == v && plainMode():
			return "[" + label + "]"
		case m.view == v:
			return tabActive.Render(" " + label + " ")
		default:
			return tabInactive.Render(" " + label + " ")
		}
	}
	return " " + tab("1 Containers", viewContainers) + " " + tab("2 Processes", viewProcesses) + " " + tab("3 Domains", viewDomains)
}

func (m Model) bodyView() string {
	if m.view == viewDomains {
		switch {
		case m.snap == nil:
			return m.message("Collecting…")
		case len(domainHosts(m.snap.Containers)) == 0:
			return m.message("No domains found.\n\nThis view lists the hosts of Traefik router rules, from container labels such as\n  traefik.http.routers.<name>.rule=Host(`app.example.com`)")
		}
		return m.domainList.view()
	}
	if m.view == viewProcesses {
		if m.snap == nil {
			return m.message("Collecting…")
		}
		return m.processes.View()
	}
	switch {
	case m.snap == nil:
		return m.message("Collecting…")
	case m.snap.DockerErr != nil:
		return m.message(fmt.Sprintf("Docker is not reachable: %v\n\nProcesses are still available (press 2), but without container names.", m.snap.DockerErr))
	case len(m.snap.Containers) == 0:
		return m.message("No containers.")
	}
	return m.containers.view()
}

// message renders a text block with the same size as the table, so the box and
// the footer keep their place.
func (m Model) message(text string) string {
	w := m.innerWidth()
	if m.logs.active {
		w = m.width
	}
	return lipgloss.NewStyle().Height(m.bodyHeight()).Render(emptyViewStyle.Width(max(w, 20)).Render(text))
}

// bodyHeight is the number of rows of the main area: the table inside the border,
// or the log viewport.
func (m Model) bodyHeight() int {
	if m.logs.active {
		return max(m.height-logsChromeLines, 4)
	}
	return m.tableHeight()
}

// diskSummaryView is the one line under the table: in the Domains view the
// domain summary, otherwise what Docker's disk use has that no container owns:
// images, build cache and what could be reclaimed.
func (m Model) diskSummaryView() string {
	if m.view == viewDomains {
		return m.domainSummaryView()
	}
	text := " disk: measuring Docker disk usage…"
	switch {
	case m.snap == nil:
		text = " disk: collecting…"
	case m.snap.Disk != nil:
		u := m.snap.Disk
		text = fmt.Sprintf(" disk: images %s · build cache %s · reclaimable %s",
			format.Bytes(uint64(u.Images)), format.Bytes(uint64(u.BuildCache)), format.Bytes(uint64(u.Reclaimable)))
	case m.snap.DiskErr != nil:
		text = " disk: Docker disk usage unavailable: " + m.snap.DiskErr.Error()
	}
	return infoStyle.MaxWidth(max(m.width, 1)).Render(text)
}

// hint is one entry of the footer: a key and what it does.
type hint struct{ key, label string }

// renderHints draws the footer: the keys in the accent color, the rest dim.
func renderHints(hs []hint) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = keyStyle.Render(h.key) + footerStyle.Render(" "+h.label)
	}
	return " " + strings.Join(parts, footerStyle.Render(" · "))
}

func (m Model) footerView() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf(" refresh failed: %v", m.err))
	}
	views, scroll, quit := hint{"1/2/3", "views"}, hint{"↑/↓", "scroll"}, hint{"q", "quit"}
	switch m.view {
	case viewDomains:
		return renderHints([]hint{views, scroll, quit})
	case viewProcesses:
		state := "hidden"
		if m.showKernel {
			state = "shown"
		}
		return renderHints([]hint{views, scroll, {"k", "kernel threads (" + state + ")"}, quit})
	}
	return renderHints([]hint{views, scroll, {"↵", "logs"}, quit})
}
