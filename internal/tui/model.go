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

const (
	refreshEvery = 2 * time.Second
	measureFor   = time.Second // CPU values are averaged over this long
)

type view int

const (
	viewContainers view = iota
	viewProcesses
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

	width, height int
	containers    list
	processes     table.Model
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
		snap, err := col.Collect(measureFor)
		return snapshotMsg{snap: snap, err: err}
	}
}

// Update handles one message and returns the new model plus an optional command.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1":
			m.view = viewContainers
			return m, nil
		case "2":
			m.view = viewProcesses
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		// The timer always keeps running; a new measurement only starts if the
		// previous one has finished (a measurement takes about a second).
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
	if m.view == viewContainers {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "up":
				m.containers.move(-1)
			case "down":
				m.containers.move(1)
			case "pgup":
				m.containers.page(-1)
			case "pgdown":
				m.containers.page(1)
			case "home":
				m.containers.top()
			case "end":
				m.containers.bottom()
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.processes, cmd = m.processes.Update(msg)
	return m, cmd
}

// layout sizes the tables to the terminal. Screen rows: info line, tabs,
// table, footer.
func (m *Model) layout() {
	const chrome = 3
	h := max(m.height-chrome, 4)
	m.containers.cols = containerColumns(m.width)
	m.containers.setSize(m.width, h)
	m.processes.SetColumns(processColumns(m.width))
	m.processes.SetHeight(h)
	m.processes.SetWidth(m.width)
}

func (m *Model) refreshRows() {
	m.containers.setRows(containerRows(m.snap.Containers))
	m.processes.SetRows(processRows(m.snap.Processes))
	// Rows can shrink between refreshes; re-set the cursor so it is clamped.
	m.processes.SetCursor(m.processes.Cursor())
}

// View renders the whole screen.
func (m Model) View() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.infoView(),
		m.tabsView(),
		m.bodyView(),
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
	s += infoStyle.Render(fmt.Sprintf(" · CPU %s · MEM %s · load %.2f %.2f %.2f",
		format.Percent(n.CPUPercent), format.Percent(n.MemPercent), n.Load1, n.Load5, n.Load15))
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
	return " " + tab("1 Containers", viewContainers) + " " + tab("2 Processes", viewProcesses)
}

func (m Model) bodyView() string {
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
	h := max(m.height-3, 4)
	return lipgloss.NewStyle().Height(h).Render(emptyViewStyle.Width(max(m.width, 20)).Render(text))
}

func (m Model) footerView() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf(" refresh failed: %v", m.err))
	}
	return footerStyle.Render(" 1/2 switch view · ↑/↓ scroll · q quit")
}
