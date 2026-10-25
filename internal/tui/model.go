// Package tui is the live terminal UI, built with Bubble Tea.
//
// Bubble Tea follows the "Elm architecture": a Model holds all state, Update
// receives messages (key presses, timer ticks, results) and returns the next
// Model, and View turns the Model into the text shown on screen.
package tui

import (
	"fmt"
	"os"
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

// chromeLines are the screen rows around the main area of the two main views:
// info line, tabs, disk summary and footer. The log screen has no disk summary.
const chromeLines = 4

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

// layout sizes the tables to the terminal.
func (m *Model) layout() {
	h := max(m.height-chromeLines, 4)
	m.containers.cols = containerColumns(m.width)
	m.containers.setSize(m.width, h)
	m.domainList.plain = true
	m.domainList.cols = domainColumns(m.width)
	m.domainList.setSize(m.width, h)
	m.processes.SetColumns(processColumns(m.width))
	m.processes.SetHeight(h)
	m.processes.SetWidth(m.width)
}

func (m *Model) refreshRows() {
	m.containers.setRows(containerRows(m.snap.Containers, m.snap.Disk, m.snap.DiskErr))
	m.processes.SetRows(processRows(m.snap.Processes, m.showKernel))
	m.refreshDomainRows()
	// Rows can shrink between refreshes; re-set the cursor so it is clamped.
	m.processes.SetCursor(m.processes.Cursor())
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
		m.bodyView(),
		m.diskSummaryView(),
		m.footerView(),
	)
}

func (m Model) infoView() string {
	s := titleStyle.Render(" EternalShell")
	if m.host != "" {
		s += infoStyle.Render(" · " + m.host)
	}
	if m.snap == nil {
		return s + infoStyle.Render(" · collecting…")
	}
	n := m.snap
	cpu := "-"
	if n.CPUKnown {
		cpu = format.Percent(n.CPUPercent)
	}
	s += infoStyle.Render(fmt.Sprintf(" · CPU %s · MEM %s · load %.2f %.2f %.2f",
		cpu, format.Percent(n.MemPercent), n.Load1, n.Load5, n.Load15))
	if root := rootMount(n.Mounts); root != nil {
		s += infoStyle.Render(fmt.Sprintf(" · disk %s %s", root.Path, format.Percent(root.UsedPercent)))
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
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

func (m Model) tabsView() string {
	tab := func(label string, v view) string {
		if m.view == v {
			return tabActive.Render("[" + label + "]")
		}
		return tabInactive.Render(" " + label + " ")
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

// message renders a text block with the same height as the tables, so the
// footer stays at the bottom.
func (m Model) message(text string) string {
	h := m.bodyHeight()
	return lipgloss.NewStyle().Height(h).Render(emptyViewStyle.Width(max(m.width, 20)).Render(text))
}

// bodyHeight is the number of rows between the title lines and the footer.
func (m Model) bodyHeight() int {
	if m.logs.active {
		return max(m.height-(chromeLines-1), 4) // the log screen has no disk summary
	}
	return max(m.height-chromeLines, 4)
}

// diskSummaryView is the one line about Docker's disk use that is not part of
// any container: images, build cache and what could be reclaimed.
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

func (m Model) footerView() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf(" refresh failed: %v", m.err))
	}
	keys := " 1/2/3 switch view · ↑/↓ scroll · enter logs · q quit"
	if m.view == viewDomains {
		keys = " 1/2/3 switch view · ↑/↓ scroll · q quit"
	}
	if m.view == viewProcesses {
		state := "hidden"
		if m.showKernel {
			state = "shown"
		}
		keys = " 1/2/3 switch view · ↑/↓ scroll · k kernel threads (" + state + ") · q quit"
	}
	return footerStyle.Render(keys)
}
